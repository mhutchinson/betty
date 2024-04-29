CREATE TABLE IF NOT EXISTS Checkpoint(
  Lock  CHAR(1) NOT NULL DEFAULT 'X',
  Note  MEDIUMBLOB NOT NULL,
  CONSTRAINT PK_T1 PRIMARY KEY (Lock),
  CONSTRAINT CK_T1_Locked CHECK (Lock='X')
);

-- An interior node of the hash tree
CREATE TABLE IF NOT EXISTS Subtree(
  SubtreeId            VARBINARY(255) NOT NULL,
  Nodes                MEDIUMBLOB NOT NULL,
  PRIMARY KEY(SubtreeId),
);

CREATE TABLE IF NOT EXISTS SequencedLeafData(
  SequenceNumber       BIGINT UNSIGNED NOT NULL,
  Data                 LONGBLOB NOT NULL,
  PRIMARY KEY(SequenceNumber),
);

