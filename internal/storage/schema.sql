-- Loom schema v2
-- A folder of files on disk is the source of truth. SQLite is just an index:
-- one row per file, with a summary + keywords produced once at scan time, plus
-- an FTS5 virtual table for ranked retrieval.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS files (
  id          INTEGER PRIMARY KEY,
  rel_path    TEXT    NOT NULL UNIQUE,
  hash        TEXT    NOT NULL,
  mtime       INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  title       TEXT    NOT NULL DEFAULT '',
  content     TEXT    NOT NULL DEFAULT '',
  summary     TEXT    NOT NULL DEFAULT '',
  keywords    TEXT    NOT NULL DEFAULT '[]',
  indexed_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_kind ON files(kind);

CREATE VIRTUAL TABLE IF NOT EXISTS files_fts USING fts5(
  title, summary, keywords, content,
  content='files',
  content_rowid='id',
  tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS files_ai AFTER INSERT ON files BEGIN
  INSERT INTO files_fts(rowid, title, summary, keywords, content)
  VALUES (new.id, new.title, new.summary, new.keywords, new.content);
END;

CREATE TRIGGER IF NOT EXISTS files_ad AFTER DELETE ON files BEGIN
  INSERT INTO files_fts(files_fts, rowid, title, summary, keywords, content)
  VALUES ('delete', old.id, old.title, old.summary, old.keywords, old.content);
END;

CREATE TRIGGER IF NOT EXISTS files_au AFTER UPDATE ON files BEGIN
  INSERT INTO files_fts(files_fts, rowid, title, summary, keywords, content)
  VALUES ('delete', old.id, old.title, old.summary, old.keywords, old.content);
  INSERT INTO files_fts(rowid, title, summary, keywords, content)
  VALUES (new.id, new.title, new.summary, new.keywords, new.content);
END;

-- Optional per-file embedding for hybrid (BM25 + vector) search.
-- Empty unless the [embeddings] config block is enabled. One vector per file;
-- ON DELETE CASCADE keeps it in sync when a file row is removed. `vec` is a
-- packed little-endian float32 blob; `dim` lets vector search skip rows indexed
-- with a different model after a model change (graceful until `loom scan --force`).
CREATE TABLE IF NOT EXISTS file_vectors (
  file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
  dim     INTEGER NOT NULL,
  model   TEXT    NOT NULL DEFAULT '',
  vec     BLOB    NOT NULL
);

CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER PRIMARY KEY,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
