-- Loom schema v1
-- SQLite storage for LLM-curated knowledge base.
-- No embeddings. FTS5 (BM25) + graph links drive all retrieval.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

-- ---------------------------------------------------------------------------
-- sources: immutable raw documents (PDF, HTML, MD, TXT, DOCX, URL)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sources (
  id          INTEGER PRIMARY KEY,
  uri         TEXT    NOT NULL UNIQUE,
  kind        TEXT    NOT NULL,
  title       TEXT,
  content     TEXT    NOT NULL,
  hash        TEXT    NOT NULL,
  ingested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  metadata    TEXT
);

CREATE INDEX IF NOT EXISTS idx_sources_hash ON sources(hash);
CREATE INDEX IF NOT EXISTS idx_sources_kind ON sources(kind);

-- ---------------------------------------------------------------------------
-- notes: LLM-curated knowledge units (mutable)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS notes (
  id         INTEGER PRIMARY KEY,
  slug       TEXT    NOT NULL UNIQUE,
  title      TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  content    TEXT    NOT NULL,
  summary    TEXT    NOT NULL DEFAULT '',
  keywords   TEXT    NOT NULL DEFAULT '[]',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  version    INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_notes_kind ON notes(kind);

-- ---------------------------------------------------------------------------
-- note_versions: snapshot history for audit and rollback
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS note_versions (
  id         INTEGER PRIMARY KEY,
  note_id    INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  version    INTEGER NOT NULL,
  content    TEXT    NOT NULL,
  changed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  reason     TEXT,
  UNIQUE(note_id, version)
);

-- ---------------------------------------------------------------------------
-- links: directed edges between notes and to sources
-- Exactly one of from_note_id / from_source_id is set, same for to_*.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS links (
  id             INTEGER PRIMARY KEY,
  from_note_id   INTEGER REFERENCES notes(id)   ON DELETE CASCADE,
  from_source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE,
  to_note_id     INTEGER REFERENCES notes(id)   ON DELETE CASCADE,
  to_source_id   INTEGER REFERENCES sources(id) ON DELETE CASCADE,
  kind           TEXT    NOT NULL,
  context        TEXT,
  CHECK (
    (from_note_id IS NOT NULL) + (from_source_id IS NOT NULL) = 1
    AND
    (to_note_id IS NOT NULL) + (to_source_id IS NOT NULL) = 1
  )
);

CREATE INDEX IF NOT EXISTS idx_links_from_note   ON links(from_note_id);
CREATE INDEX IF NOT EXISTS idx_links_from_source ON links(from_source_id);
CREATE INDEX IF NOT EXISTS idx_links_to_note     ON links(to_note_id);
CREATE INDEX IF NOT EXISTS idx_links_to_source   ON links(to_source_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_links_nn
  ON links(from_note_id, to_note_id, kind)
  WHERE from_note_id IS NOT NULL AND to_note_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS ux_links_ns
  ON links(from_note_id, to_source_id, kind)
  WHERE from_note_id IS NOT NULL AND to_source_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS ux_links_sn
  ON links(from_source_id, to_note_id, kind)
  WHERE from_source_id IS NOT NULL AND to_note_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- chunks: token-sized text slices, source of truth for the FTS index
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS chunks (
  id         INTEGER PRIMARY KEY,
  source_id  INTEGER REFERENCES sources(id) ON DELETE CASCADE,
  note_id    INTEGER REFERENCES notes(id)   ON DELETE CASCADE,
  content    TEXT    NOT NULL,
  position   INTEGER NOT NULL,
  tokens     INTEGER,
  CHECK ((source_id IS NOT NULL) + (note_id IS NOT NULL) = 1)
);

CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_note   ON chunks(note_id);

-- ---------------------------------------------------------------------------
-- search_index: FTS5 virtual table, BM25 ranking over all searchable text
-- entity_ref encodes "note:<id>", "source:<id>", or "chunk:<id>" so a single
-- hit can be resolved back to its origin.
-- ---------------------------------------------------------------------------
CREATE VIRTUAL TABLE IF NOT EXISTS search_index USING fts5(
  title,
  content,
  keywords,
  summary,
  kind        UNINDEXED,
  entity_ref  UNINDEXED,
  tokenize = 'porter unicode61 remove_diacritics 2'
);

-- ---------------------------------------------------------------------------
-- tags and note<->tag join
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tags (
  id   INTEGER PRIMARY KEY,
  name TEXT    NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS note_tags (
  note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  PRIMARY KEY (note_id, tag_id)
);

-- ---------------------------------------------------------------------------
-- aliases: LLM-resolved co-references ("AK" -> "andrej-karpathy")
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS aliases (
  id      INTEGER PRIMARY KEY,
  note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  alias   TEXT    NOT NULL,
  UNIQUE (note_id, alias)
);

CREATE INDEX IF NOT EXISTS idx_aliases_alias ON aliases(alias);

-- ---------------------------------------------------------------------------
-- operations: append-only log of ingest, query, lint, edit actions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS operations (
  id      INTEGER PRIMARY KEY,
  kind    TEXT    NOT NULL,
  at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  actor   TEXT,
  summary TEXT    NOT NULL,
  details TEXT
);

CREATE INDEX IF NOT EXISTS idx_operations_kind_at ON operations(kind, at);

-- ---------------------------------------------------------------------------
-- schema_version: single-row table used by the Go migration runner
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS schema_version (
  version    INTEGER PRIMARY KEY,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
