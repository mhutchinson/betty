package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhutchinson/tlog-lite/log"
	"github.com/mhutchinson/tlog-lite/storage/tsql"
	f_log "github.com/transparency-dev/formats/log"
	"golang.org/x/mod/sumdb/note"
	"k8s.io/klog/v2"
)

var (
	sqlConnString = flag.String("mysql_uri", "user:password@tcp(db:3306)/litelog", "Connection string for a MySQL database")
	batchSize     = flag.Int("batch_size", 1, "Size of batch before flushing")
	batchMaxAge   = flag.Duration("batch_max_age", 100*time.Millisecond, "Max age for batch entries before flushing")

	listen = flag.String("listen", ":2024", "Address:port to listen on")

	signer   = flag.String("log_signer", "PRIVATE+KEY+Test-Betty+df84580a+Afge8kCzBXU7jb3cV2Q363oNXCufJ6u9mjOY1BGRY9E2", "Log signer")
	verifier = flag.String("log_verifier", "Test-Betty+df84580a+AQQASqPUZoIHcJAF5mBOryctwFdTV1E0GRY4kEAtTzwB", "log verifier")
)

type latency struct {
	sync.Mutex
	total time.Duration
	n     int
	min   time.Duration
	max   time.Duration
}

func (l *latency) Add(d time.Duration) {
	l.Lock()
	defer l.Unlock()
	l.total += d
	l.n++
	if d < l.min {
		l.min = d
	}
	if d > l.max {
		l.max = d
	}
}

func (l *latency) String() string {
	l.Lock()
	defer l.Unlock()
	if l.n == 0 {
		return "--"
	}
	return fmt.Sprintf("[Mean: %v Min: %v Max %v]", l.total/time.Duration(l.n), l.min, l.max)
}

func keysFromFlag() (note.Signer, note.Verifier) {
	sKey, err := note.NewSigner(*signer)
	if err != nil {
		klog.Exitf("Invalid signing key: %v", err)
	}
	vKey, err := note.NewVerifier(*verifier)
	if err != nil {
		klog.Exitf("Invalid verifier key: %v", err)
	}
	return sKey, vKey
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	ctx := context.Background()

	sKey, vKey := keysFromFlag()
	parse := parseCheckpoint(vKey)
	create := newTree(sKey)
	s := tsql.New(*sqlConnString, log.Params{EntryBundleSize: *batchSize}, *batchMaxAge, parse, create)

	if _, err := s.ReadCheckpoint(); err != nil {
		klog.Infof("ct: %v", err)
		if cp, err := create(0, []byte("Empty")); err != nil {
			klog.Exitf("Failed to initialise log: %v", err)
		} else {
			if err := s.WriteCheckpoint(ctx, cp); err != nil {
				klog.Exitf("Failed to write initial checkpoint: %v", err)
			}
		}
	}

	l := &latency{}

	http.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		n := time.Now()
		defer func() { l.Add(time.Since(n)) }()

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()
		idx, err := s.Sequence(ctx, b)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to sequence entry: %v", err)))
			return
		}
		w.Write([]byte(fmt.Sprintf("%d\n", idx)))
	})
	http.HandleFunc("GET /checkpoint", func(w http.ResponseWriter, r *http.Request) {
		klog.V(2).Info("Getting checkpoint")
		cp, err := s.ReadCheckpoint()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(cp)
	})
	http.HandleFunc("GET /tile/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		level, index, partial, err := parseTilePath(path)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(fmt.Sprintf("Failed to parse tile path: %v", err)))
			return
		}
		klog.V(2).Infof("Getting tile level=%d, index=%d (partial=%d)", level, index, partial)

		tile, err := s.GetTile(r.Context(), level, index)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to get tile: %v", err)))
			return
		}
		b, err := tile.MarshalText()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to get tile: %v", err)))
			return
		}
		w.Write(b)
	})
	http.HandleFunc("GET /seq/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := "/" + r.PathValue("path")
		if path[0] != os.PathSeparator || path[3] != os.PathSeparator || path[6] != os.PathSeparator || path[9] != os.PathSeparator || path[12] != os.PathSeparator {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Failed to parse seq path"))
			return
		}
		var b strings.Builder
		for _, s := range []string{path[1:3], path[4:6], path[7:9], path[10:12], path[13:]} {
			b.WriteString(s)
		}
		index, err := strconv.ParseUint(b.String(), 16, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Failed to parse seq path"))
			return
		}
		klog.V(2).Infof("Getting leaf index=%d", index)

		tile, err := s.GetEntryBundle(r.Context(), index)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to get tile: %v", err)))
			return
		}
		w.Write(tile)
	})

	// go printStats(ctx, s, parse, l)
	if err := http.ListenAndServe(*listen, http.DefaultServeMux); err != nil {
		klog.Exitf("ListenAndServe: %v", err)
	}
}

// parseTilePath is the opposite of layout.TilePath().
func parseTilePath(path string) (level, index, partialTileSize uint64, err error) {
	parts := strings.Split(path, "/")
	if len(parts) != 5 {
		return 0, 0, 0, fmt.Errorf("Malformed path: %v", path)
	}
	// parse level
	t := new(big.Int)
	if _, ok := t.SetString(parts[0], 16); !ok {
		return 0, 0, 0, fmt.Errorf("Malformed level: %v", parts[0])
	}
	level = t.Uint64()

	// parse partial tile
	lastParts := strings.Split(parts[4], ".")
	if len(lastParts) > 2 {
		return 0, 0, 0, fmt.Errorf("Malformed final part: %v", parts[4])
	}
	parts[4] = lastParts[0]
	if len(lastParts) == 2 {
		if _, ok := t.SetString(lastParts[1], 16); !ok {
			return 0, 0, 0, fmt.Errorf("Malformed final part: %v", parts[4])
		}
		partialTileSize = t.Uint64()
	}
	// parse index
	indexStr := strings.Join(parts[1:5], "")
	if _, ok := t.SetString(indexStr, 16); !ok {
		return 0, 0, 0, fmt.Errorf("Malformed index: %v", indexStr)
	}
	index = t.Uint64()
	return level, index, partialTileSize, nil
}

func parseCheckpoint(verifier note.Verifier) tsql.ParseCheckpointFunc {
	return func(raw []byte) (uint64, error) {
		cp, _, _, err := f_log.ParseCheckpoint(raw, verifier.Name(), verifier)
		if err != nil {
			return 0, err
		}
		return cp.Size, nil
	}
}

func newTree(signer note.Signer) tsql.CreateCheckpointFunc {
	return func(size uint64, hash []byte) ([]byte, error) {
		cp := &f_log.Checkpoint{
			Origin: signer.Name(),
			Size:   size,
			Hash:   hash,
		}
		return note.Sign(&note.Note{Text: string(cp.Marshal())}, signer)
	}
}

func printStats(ctx context.Context, s *tsql.Storage, parse tsql.ParseCheckpointFunc, l *latency) {
	interval := time.Second
	var lastSize uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			raw, err := s.ReadCheckpoint()
			if err != nil {
				klog.Errorf("Failed to get checkpoint: %v", err)
				continue
			}
			size, err := parse(raw)
			if err != nil {
				klog.Errorf("Failed to parse checkpoint: %v", err)
			}
			if lastSize > 0 {
				added := size - lastSize
				klog.Infof("CP size %d (+%d); Latency: %v", size, added, l.String())
			}
			lastSize = size
		}
	}
}
