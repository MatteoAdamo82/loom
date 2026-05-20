// Package storage owns the SQLite index. The filesystem holds the truth (one
// folder of files); the DB just speeds up lookups and keeps LLM-generated
// summaries+keywords next to each file.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("storage: not found")

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := path
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared&_pragma=foreign_keys(1)"
	} else {
		dsn = fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// Upsert writes (or replaces) the row keyed by RelPath. The triggers on
// `files` keep the FTS index in sync.
func (s *Store) Upsert(ctx context.Context, f *File) error {
	kw, err := json.Marshal(f.Keywords)
	if err != nil {
		return fmt.Errorf("marshal keywords: %w", err)
	}
	if len(f.Keywords) == 0 {
		kw = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO files (rel_path, hash, mtime, kind, title, content, summary, keywords)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(rel_path) DO UPDATE SET
			hash       = excluded.hash,
			mtime      = excluded.mtime,
			kind       = excluded.kind,
			title      = excluded.title,
			content    = excluded.content,
			summary    = excluded.summary,
			keywords   = excluded.keywords,
			indexed_at = CURRENT_TIMESTAMP
	`, f.RelPath, f.Hash, f.MTime, f.Kind, f.Title, f.Content, f.Summary, string(kw))
	return err
}

func (s *Store) Delete(ctx context.Context, relPath string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE rel_path = ?`, relPath)
	return err
}

// GetByRelPath returns the file row, or ErrNotFound.
func (s *Store) GetByRelPath(ctx context.Context, relPath string) (*File, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, rel_path, hash, mtime, kind, title, content, summary, keywords, indexed_at
		FROM files WHERE rel_path = ?`, relPath)
	return scanFile(row.Scan)
}

// List returns all files ordered by rel_path. Used by the GUI sidebar and the
// scan reconciliation pass.
func (s *Store) List(ctx context.Context) ([]*File, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rel_path, hash, mtime, kind, title, content, summary, keywords, indexed_at
		FROM files ORDER BY rel_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*File
	for rows.Next() {
		f, err := scanFile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Search runs an FTS5 BM25 query and returns up to limit hits, ordered best
// first. Empty query returns ErrNotFound.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]*Hit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, ErrNotFound
	}
	if limit <= 0 {
		limit = 5
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.rel_path, f.hash, f.mtime, f.kind, f.title, f.content,
		       f.summary, f.keywords, f.indexed_at, bm25(files_fts) AS rank
		FROM files_fts
		JOIN files f ON f.id = files_fts.rowid
		WHERE files_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, ftsQuery(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Hit
	for rows.Next() {
		var h Hit
		var keywordsJSON string
		if err := rows.Scan(
			&h.ID, &h.RelPath, &h.Hash, &h.MTime, &h.Kind, &h.Title, &h.Content,
			&h.Summary, &keywordsJSON, &h.IndexedAt, &h.Rank,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(keywordsJSON), &h.Keywords)
		out = append(out, &h)
	}
	return out, rows.Err()
}

// Count returns the total number of indexed files.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&n)
	return n, err
}

// scanFile decodes a row into a File. The argument matches both *sql.Row.Scan
// and *sql.Rows.Scan signatures.
func scanFile(scan func(...any) error) (*File, error) {
	var f File
	var keywordsJSON string
	err := scan(
		&f.ID, &f.RelPath, &f.Hash, &f.MTime, &f.Kind, &f.Title, &f.Content,
		&f.Summary, &keywordsJSON, &f.IndexedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(keywordsJSON), &f.Keywords)
	return &f, nil
}

// ftsQuery prepares user input for SQLite FTS5. It strips characters that
// would otherwise be interpreted as syntax (quotes, parens, colons, the NEAR
// operator, etc.) and joins the remaining tokens with OR so that a hit on any
// term still matches. Anything that becomes empty after sanitisation falls
// back to a single space, which makes FTS return zero rows rather than error.
func ftsQuery(q string) string {
	repl := strings.NewReplacer(
		`"`, " ", `'`, " ", `(`, " ", `)`, " ",
		`:`, " ", `*`, " ", `^`, " ", `-`, " ",
		`+`, " ", `.`, " ", `,`, " ", `?`, " ",
		`!`, " ", `;`, " ",
	)
	cleaned := repl.Replace(q)
	fields := strings.Fields(cleaned)
	if len(fields) == 0 {
		return " "
	}
	for i, f := range fields {
		fields[i] = `"` + f + `"`
	}
	return strings.Join(fields, " OR ")
}
