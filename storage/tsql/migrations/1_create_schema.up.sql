CREATE TABLE IF NOT EXISTS Checkpoint(
  Id    INT UNSIGNED NOT NULL,
  Note  MEDIUMBLOB NOT NULL,
  PRIMARY KEY(Id)
);

-- An interior node of the hash tree
CREATE TABLE IF NOT EXISTS Subtree(
  Level                INT UNSIGNED NOT NULL,
  Idx                  BIGINT UNSIGNED NOT NULL,
  Nodes                MEDIUMBLOB NOT NULL,
  PRIMARY KEY(Level, Idx)
);

CREATE TABLE IF NOT EXISTS TiledLeaves(
  TileIdx              BIGINT UNSIGNED NOT NULL,
  Data                 LONGBLOB NOT NULL,
  PRIMARY KEY(TileIdx)
);

