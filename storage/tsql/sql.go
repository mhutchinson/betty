package tsql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mhutchinson/tlog-lite/log"
	"github.com/mhutchinson/tlog-lite/log/writer"
	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/serverless-log/api"
	"k8s.io/klog/v2"

	_ "github.com/go-sql-driver/mysql"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// CreateCheckpointFunc is the signature of a function that creates a new checkpoint for the given size and hash.
type CreateCheckpointFunc func(size uint64, root []byte) ([]byte, error)

// ParseCheckpointFunc is the signature of a function which parses the current integrated tree size
type ParseCheckpointFunc func([]byte) (uint64, error)

// New creates a new SQL storage.
func New(connection string, params log.Params, batchMaxAge time.Duration, parseCheckpoint ParseCheckpointFunc, createCheckpoint CreateCheckpointFunc) *Storage {
	db, err := sql.Open("mysql", connection)
	if err != nil {
		panic(err)
	}
	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.Ping(); err != nil {
		panic(err)
	}
	r := &Storage{
		db:               db,
		params:           params,
		parseCheckpoint:  parseCheckpoint,
		createCheckpoint: createCheckpoint,
	}
	r.pool = writer.NewPool(params.EntryBundleSize, batchMaxAge, r.sequenceBatch)

	return r
}

// Storage implements storage functions for a POSIX filesystem.
// It leverages the POSIX atomic operations.
type Storage struct {
	sync.Mutex
	params log.Params
	db     *sql.DB
	pool   *writer.Pool

	cpFile *os.File

	parseCheckpoint  ParseCheckpointFunc
	createCheckpoint CreateCheckpointFunc
}

// Sequence commits to sequence numbers for an entry
// Returns the sequence number assigned to the first entry in the batch, or an error.
func (s *Storage) Sequence(ctx context.Context, b []byte) (uint64, error) {
	return s.pool.Add(b)
}

// GetEntryBundle retrieves the Nth entries bundle.
func (s *Storage) GetEntryBundle(ctx context.Context, index uint64) ([]byte, error) {
	row := s.db.QueryRow("SELECT Data FROM TiledLeaves WHERE TileIdx = ?", index)
	var part []byte
	err := row.Scan(&part)
	return part, err
}

// sequenceBatch writes the entries from the provided batch into the entry bundle files of the log.
//
// This func starts filling entries bundles at the next available slot in the log, ensuring that the
// sequenced entries are contiguous from the zeroth entry (i.e left-hand dense).
// We try to minimise the number of partially complete entry bundles by writing entries in chunks rather
// than one-by-one.
func (s *Storage) sequenceBatch(ctx context.Context, batch writer.Batch) (uint64, int, error) {
	if len(batch.Entries) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		ReadOnly: false,
	})
	if err != nil {
		return 0, 0, err
	}
	startTime := time.Now()
	defer func() {
		if err := tx.Rollback(); err != nil {
			klog.V(2).Infof("Error rolling back TX: %v", err)
		}
	}()

	row := tx.QueryRow("SELECT Note FROM Checkpoint WHERE Id = ?", checkpointID)
	var cp []byte
	if err := row.Scan(&cp); err != nil {
		return 0, 0, fmt.Errorf("failed to read checkpoint: %v", err)
	}
	size, err := s.parseCheckpoint(cp)
	if err != nil {
		return 0, 0, err
	}
	bundleIndex, entriesInBundle := size/uint64(s.params.EntryBundleSize), size%uint64(s.params.EntryBundleSize)
	idealNexBatch := s.params.EntryBundleSize - int(entriesInBundle)
	bundle := &bytes.Buffer{}
	if entriesInBundle > 0 {
		klog.V(2).Infof("Bundle sizes not aligned, need to read %d leaves", entriesInBundle)
		row := tx.QueryRow("SELECT Data FROM TiledLeaves WHERE TileIdx = ?", bundleIndex)
		var part []byte
		if err := row.Scan(&part); err != nil {
			return 0, 0, err
		}
		bundle.Write(part)
	}
	// Add new entries to the bundle
	for _, e := range batch.Entries {
		bundle.WriteString(base64.StdEncoding.EncodeToString(e))
		bundle.WriteString("\n")
		entriesInBundle++
		if entriesInBundle == uint64(s.params.EntryBundleSize) {
			//  This bundle is full, so we need to write it out...
			_, err := tx.ExecContext(ctx, "REPLACE INTO TiledLeaves (TileIdx, Data) VALUES (?, ?)", bundleIndex, bundle.Bytes())
			if err != nil {
				return 0, 0, err
			}

			// ... and prepare the next entry bundle for any remaining entries in the batch
			bundleIndex++
			entriesInBundle = 0
			bundle = &bytes.Buffer{}
		}
	}
	// If we have a partial bundle remaining once we've added all the entries from the batch,
	// this needs writing out too.
	if entriesInBundle > 0 {
		_, err := tx.ExecContext(ctx, "REPLACE INTO TiledLeaves (TileIdx, Data) VALUES (?, ?)", bundleIndex, bundle.Bytes())
		if err != nil {
			return 0, 0, err
		}
	}
	// For simplicitly, we'll in-line the integration of these new entries into the Merkle structure too.
	if err := s.doIntegrateTx(ctx, tx, size, batch.Entries); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	klog.V(1).Infof("sequenceBatch time taken for %d elements: %s", len(batch.Entries), time.Since(startTime))
	return size, idealNexBatch, nil
}

type transactionalStorage struct {
	s  *Storage
	tx *sql.Tx
}

// GetTile returns the tile at the given level & index.
func (ts transactionalStorage) GetTile(ctx context.Context, level, index uint64) (*api.Tile, error) {
	return ts.s.getTileTx(ctx, ts.tx, level, index)
}

// StoreTile stores the tile at the given level & index.
func (ts transactionalStorage) StoreTile(ctx context.Context, level, index uint64, tile *api.Tile) error {
	return ts.s.storeTileTx(ctx, ts.tx, level, index, tile)

}

// doIntegrate handles integrating new entries into the log, and updating the checkpoint.
func (s *Storage) doIntegrateTx(ctx context.Context, tx *sql.Tx, from uint64, batch [][]byte) error {
	ts := transactionalStorage{
		s:  s,
		tx: tx,
	}

	newSize, newRoot, err := writer.Integrate(ctx, from, batch, ts, rfc6962.DefaultHasher)
	if err != nil {
		klog.Errorf("Failed to integrate: %v", err)
		return err
	}
	var cp []byte
	if cp, err = s.createCheckpoint(newSize, newRoot); err != nil {
		return fmt.Errorf("createCheckpoint: %v", err)
	}
	if err := s.writeCheckpointTx(ctx, tx, cp); err != nil {
		return fmt.Errorf("writeCheckpoint: %v", err)
	}
	return nil
}

// GetTile returns the tile at the given tile-level and tile-index.
// If no complete tile exists at that location, it will attempt to find a
// partial tile for the given tree size at that location.
func (s *Storage) GetTile(ctx context.Context, level, index uint64) (*api.Tile, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			klog.V(2).Infof("Error rolling back TX: %v", err)
		}
	}()

	tile, err := s.getTileTx(ctx, tx, level, index)
	if err != nil {
		return tile, err
	}
	return tile, tx.Commit()
}

func (s *Storage) getTileTx(ctx context.Context, tx *sql.Tx, level, index uint64) (*api.Tile, error) {
	row := tx.QueryRowContext(ctx, "SELECT Nodes FROM Subtree WHERE Level = ? AND Idx = ?", level, index)
	var nodes []byte
	if err := row.Scan(&nodes); err != nil {
		if err != sql.ErrNoRows {
			return nil, err
		}
		return nil, os.ErrNotExist
	}

	var tile api.Tile
	if err := tile.UnmarshalText(nodes); err != nil {
		return nil, fmt.Errorf("failed to parse tile: %w", err)
	}
	return &tile, nil
}

func (s *Storage) StoreTile(ctx context.Context, level, index uint64, tile *api.Tile) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false})
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			klog.V(2).Infof("Error rolling back TX: %v", err)
		}
	}()
	err = s.storeTileTx(ctx, tx, level, index, tile)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) storeTileTx(ctx context.Context, tx *sql.Tx, level, index uint64, tile *api.Tile) error {
	tileSize := uint64(tile.NumLeaves)
	klog.V(2).Infof("StoreTile: level %d index %x ts: %x", level, index, tileSize)
	if tileSize == 0 || tileSize > 256 {
		return fmt.Errorf("tileSize %d must be > 0 and <= 256", tileSize)
	}
	t, err := tile.MarshalText()
	if err != nil {
		return fmt.Errorf("failed to marshal tile: %w", err)
	}
	_, err = tx.ExecContext(ctx, "REPLACE INTO Subtree (Level, Idx, Nodes) VALUES (?, ?, ?)", level, index, t)
	return err
}

const checkpointID = 0

// WriteCheckpoint stores a raw log checkpoint on disk.
func (s *Storage) WriteCheckpoint(ctx context.Context, newCPRaw []byte) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false})
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			klog.V(2).Infof("Error rolling back TX: %v", err)
		}
	}()
	err = s.writeCheckpointTx(ctx, tx, newCPRaw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) writeCheckpointTx(_ context.Context, tx *sql.Tx, newCPRaw []byte) error {
	_, err := tx.Exec("REPLACE INTO Checkpoint (Id, Note) VALUES (?, ?)", checkpointID, newCPRaw)
	return err
}

// Readcheckpoint returns the latest stored checkpoint.
func (s *Storage) ReadCheckpoint() ([]byte, error) {
	row := s.db.QueryRow("SELECT Note FROM Checkpoint WHERE Id = ?", checkpointID)
	var cp []byte
	return cp, row.Scan(&cp)
}

func (s *Storage) String() string {
	return fmt.Sprintf("Total time blocked on awaiting DB connections: %s", s.db.Stats().WaitDuration)
}
