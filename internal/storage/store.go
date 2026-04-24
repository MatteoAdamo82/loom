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

// execer is the minimal surface we need to run queries; both *sql.DB and
// *sql.Tx implement it, which lets the CRUD helpers below work transparently
// inside or outside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db *sql.DB
}

// Tx is a transactional handle that exposes the same CRUD surface as Store
// but batches writes under a single SQLite transaction.
type Tx struct {
	tx *sql.Tx
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

// WithTx runs fn inside a SQLite transaction. If fn returns an error, the
// transaction is rolled back and that error is returned; otherwise the
// transaction is committed.
func (s *Store) WithTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(&Tx{tx: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// sources
// ---------------------------------------------------------------------------

func createSource(ctx context.Context, q execer, src *Source) error {
	res, err := q.ExecContext(ctx,
		`INSERT INTO sources (uri, kind, title, content, hash, metadata)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		src.URI, src.Kind, src.Title, src.Content, src.Hash, nullStringJSON(src.Metadata),
	)
	if err != nil {
		return fmt.Errorf("insert source: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	src.ID = id
	return nil
}

func (s *Store) CreateSource(ctx context.Context, src *Source) error {
	return createSource(ctx, s.db, src)
}
func (t *Tx) CreateSource(ctx context.Context, src *Source) error {
	return createSource(ctx, t.tx, src)
}

func getSourceByHash(ctx context.Context, q execer, hash string) (*Source, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, uri, kind, title, content, hash, ingested_at, metadata
		   FROM sources WHERE hash = ?`, hash)
	return scanSource(row)
}

func (s *Store) GetSourceByHash(ctx context.Context, hash string) (*Source, error) {
	return getSourceByHash(ctx, s.db, hash)
}
func (t *Tx) GetSourceByHash(ctx context.Context, hash string) (*Source, error) {
	return getSourceByHash(ctx, t.tx, hash)
}

func getSource(ctx context.Context, q execer, id int64) (*Source, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, uri, kind, title, content, hash, ingested_at, metadata
		   FROM sources WHERE id = ?`, id)
	return scanSource(row)
}

func (s *Store) GetSource(ctx context.Context, id int64) (*Source, error) {
	return getSource(ctx, s.db, id)
}
func (t *Tx) GetSource(ctx context.Context, id int64) (*Source, error) {
	return getSource(ctx, t.tx, id)
}

// ---------------------------------------------------------------------------
// notes
// ---------------------------------------------------------------------------

func createNote(ctx context.Context, q execer, n *Note) error {
	kw, err := json.Marshal(n.Keywords)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx,
		`INSERT INTO notes (slug, title, kind, content, summary, keywords)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		n.Slug, n.Title, n.Kind, n.Content, n.Summary, string(kw),
	)
	if err != nil {
		return fmt.Errorf("insert note: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	n.ID = id
	n.Version = 1
	return nil
}

func (s *Store) CreateNote(ctx context.Context, n *Note) error {
	return createNote(ctx, s.db, n)
}
func (t *Tx) CreateNote(ctx context.Context, n *Note) error {
	return createNote(ctx, t.tx, n)
}

func getNoteBySlug(ctx context.Context, q execer, slug string) (*Note, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, slug, title, kind, content, summary, keywords, created_at, updated_at, version
		   FROM notes WHERE slug = ?`, slug)
	return scanNote(row)
}

func (s *Store) GetNoteBySlug(ctx context.Context, slug string) (*Note, error) {
	return getNoteBySlug(ctx, s.db, slug)
}
func (t *Tx) GetNoteBySlug(ctx context.Context, slug string) (*Note, error) {
	return getNoteBySlug(ctx, t.tx, slug)
}

func getNote(ctx context.Context, q execer, id int64) (*Note, error) {
	row := q.QueryRowContext(ctx,
		`SELECT id, slug, title, kind, content, summary, keywords, created_at, updated_at, version
		   FROM notes WHERE id = ?`, id)
	return scanNote(row)
}

func (s *Store) GetNote(ctx context.Context, id int64) (*Note, error) {
	return getNote(ctx, s.db, id)
}
func (t *Tx) GetNote(ctx context.Context, id int64) (*Note, error) {
	return getNote(ctx, t.tx, id)
}

// updateNoteInline archives the current version and writes the new one under
// the provided execer; the caller is responsible for transactionality if
// atomicity matters.
func updateNoteInline(ctx context.Context, q execer, n *Note, reason string) error {
	var prevContent string
	var prevVersion int
	err := q.QueryRowContext(ctx,
		`SELECT content, version FROM notes WHERE id = ?`, n.ID,
	).Scan(&prevContent, &prevVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	if _, err := q.ExecContext(ctx,
		`INSERT INTO note_versions (note_id, version, content, reason)
		 VALUES (?, ?, ?, ?)`,
		n.ID, prevVersion, prevContent, reason,
	); err != nil {
		return fmt.Errorf("archive prev version: %w", err)
	}

	kw, err := json.Marshal(n.Keywords)
	if err != nil {
		return err
	}
	newVersion := prevVersion + 1
	_, err = q.ExecContext(ctx,
		`UPDATE notes
		    SET title = ?, kind = ?, content = ?, summary = ?, keywords = ?,
		        updated_at = CURRENT_TIMESTAMP, version = ?
		  WHERE id = ?`,
		n.Title, n.Kind, n.Content, n.Summary, string(kw), newVersion, n.ID,
	)
	if err != nil {
		return fmt.Errorf("update note: %w", err)
	}
	n.Version = newVersion
	return nil
}

// UpdateNote runs the archive+update under a dedicated transaction so the two
// writes stay atomic when called outside of an outer tx.
func (s *Store) UpdateNote(ctx context.Context, n *Note, reason string) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		return updateNoteInline(ctx, tx.tx, n, reason)
	})
}

// UpdateNote on *Tx trusts the caller's outer transaction and doesn't open a
// nested one.
func (t *Tx) UpdateNote(ctx context.Context, n *Note, reason string) error {
	return updateNoteInline(ctx, t.tx, n, reason)
}

func listNotes(ctx context.Context, q execer, kind string, limit, offset int) ([]*Note, error) {
	query := `SELECT id, slug, title, kind, content, summary, keywords, created_at, updated_at, version
	            FROM notes`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListNotes(ctx context.Context, kind string, limit, offset int) ([]*Note, error) {
	return listNotes(ctx, s.db, kind, limit, offset)
}
func (t *Tx) ListNotes(ctx context.Context, kind string, limit, offset int) ([]*Note, error) {
	return listNotes(ctx, t.tx, kind, limit, offset)
}

// ---------------------------------------------------------------------------
// links
// ---------------------------------------------------------------------------

func createLink(ctx context.Context, q execer, l *Link) error {
	res, err := q.ExecContext(ctx,
		`INSERT INTO links (from_note_id, from_source_id, to_note_id, to_source_id, kind, context)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nullableInt(l.FromNoteID), nullableInt(l.FromSourceID),
		nullableInt(l.ToNoteID), nullableInt(l.ToSourceID),
		string(l.Kind), l.Context,
	)
	if err != nil {
		return fmt.Errorf("insert link: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	l.ID = id
	return nil
}

func (s *Store) CreateLink(ctx context.Context, l *Link) error {
	return createLink(ctx, s.db, l)
}
func (t *Tx) CreateLink(ctx context.Context, l *Link) error {
	return createLink(ctx, t.tx, l)
}

func linksFromNote(ctx context.Context, q execer, noteID int64) ([]*Link, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, from_note_id, from_source_id, to_note_id, to_source_id, kind, context
		   FROM links WHERE from_note_id = ?`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func (s *Store) LinksFromNote(ctx context.Context, noteID int64) ([]*Link, error) {
	return linksFromNote(ctx, s.db, noteID)
}
func (t *Tx) LinksFromNote(ctx context.Context, noteID int64) ([]*Link, error) {
	return linksFromNote(ctx, t.tx, noteID)
}

func linksToNote(ctx context.Context, q execer, noteID int64) ([]*Link, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, from_note_id, from_source_id, to_note_id, to_source_id, kind, context
		   FROM links WHERE to_note_id = ?`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinks(rows)
}

func (s *Store) LinksToNote(ctx context.Context, noteID int64) ([]*Link, error) {
	return linksToNote(ctx, s.db, noteID)
}
func (t *Tx) LinksToNote(ctx context.Context, noteID int64) ([]*Link, error) {
	return linksToNote(ctx, t.tx, noteID)
}

// ---------------------------------------------------------------------------
// chunks
// ---------------------------------------------------------------------------

func createChunk(ctx context.Context, q execer, c *Chunk) error {
	res, err := q.ExecContext(ctx,
		`INSERT INTO chunks (source_id, note_id, content, position, tokens)
		 VALUES (?, ?, ?, ?, ?)`,
		nullableInt(c.SourceID), nullableInt(c.NoteID),
		c.Content, c.Position, c.Tokens,
	)
	if err != nil {
		return fmt.Errorf("insert chunk: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	c.ID = id
	return nil
}

func (s *Store) CreateChunk(ctx context.Context, c *Chunk) error {
	return createChunk(ctx, s.db, c)
}
func (t *Tx) CreateChunk(ctx context.Context, c *Chunk) error {
	return createChunk(ctx, t.tx, c)
}

// ---------------------------------------------------------------------------
// FTS5 search index
// ---------------------------------------------------------------------------

func indexNote(ctx context.Context, q execer, n *Note) error {
	ref := fmt.Sprintf("note:%d", n.ID)
	if _, err := q.ExecContext(ctx,
		`DELETE FROM search_index WHERE entity_ref = ?`, ref,
	); err != nil {
		return err
	}
	kw := strings.Join(n.Keywords, " ")
	_, err := q.ExecContext(ctx,
		`INSERT INTO search_index (title, content, keywords, summary, kind, entity_ref)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		n.Title, n.Content, kw, n.Summary, n.Kind, ref,
	)
	return err
}

func (s *Store) IndexNote(ctx context.Context, n *Note) error {
	return indexNote(ctx, s.db, n)
}
func (t *Tx) IndexNote(ctx context.Context, n *Note) error {
	return indexNote(ctx, t.tx, n)
}

func indexChunk(ctx context.Context, q execer, c *Chunk, title string) error {
	ref := fmt.Sprintf("chunk:%d", c.ID)
	if _, err := q.ExecContext(ctx,
		`DELETE FROM search_index WHERE entity_ref = ?`, ref,
	); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO search_index (title, content, keywords, summary, kind, entity_ref)
		 VALUES (?, ?, '', '', 'chunk', ?)`,
		title, c.Content, ref,
	)
	return err
}

func (s *Store) IndexChunk(ctx context.Context, c *Chunk, title string) error {
	return indexChunk(ctx, s.db, c, title)
}
func (t *Tx) IndexChunk(ctx context.Context, c *Chunk, title string) error {
	return indexChunk(ctx, t.tx, c, title)
}

// Search runs a BM25 query. Higher score = more relevant (we return -rank so
// callers can sort descending).
func (s *Store) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_ref, title, snippet(search_index, 1, '<b>', '</b>', '…', 12),
		        -bm25(search_index), kind
		   FROM search_index
		  WHERE search_index MATCH ?
		  ORDER BY bm25(search_index)
		  LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.EntityRef, &h.Title, &h.Snippet, &h.Score, &h.Kind); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// operations log
// ---------------------------------------------------------------------------

func logOperation(ctx context.Context, q execer, op *Operation) error {
	res, err := q.ExecContext(ctx,
		`INSERT INTO operations (kind, actor, summary, details)
		 VALUES (?, ?, ?, ?)`,
		op.Kind, op.Actor, op.Summary, nullStringJSON(op.Details),
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	op.ID = id
	return nil
}

func (s *Store) LogOperation(ctx context.Context, op *Operation) error {
	return logOperation(ctx, s.db, op)
}
func (t *Tx) LogOperation(ctx context.Context, op *Operation) error {
	return logOperation(ctx, t.tx, op)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(row rowScanner) (*Source, error) {
	var s Source
	var meta sql.NullString
	err := row.Scan(&s.ID, &s.URI, &s.Kind, &s.Title, &s.Content, &s.Hash, &s.IngestedAt, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if meta.Valid {
		s.Metadata = json.RawMessage(meta.String)
	}
	return &s, nil
}

func scanNote(row rowScanner) (*Note, error) {
	var n Note
	var kw string
	err := row.Scan(&n.ID, &n.Slug, &n.Title, &n.Kind, &n.Content, &n.Summary, &kw,
		&n.CreatedAt, &n.UpdatedAt, &n.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if kw != "" {
		if err := json.Unmarshal([]byte(kw), &n.Keywords); err != nil {
			return nil, fmt.Errorf("decode keywords: %w", err)
		}
	}
	return &n, nil
}

func scanLinks(rows *sql.Rows) ([]*Link, error) {
	var out []*Link
	for rows.Next() {
		var l Link
		var fromNote, fromSource, toNote, toSource sql.NullInt64
		var kind, ctx sql.NullString
		if err := rows.Scan(&l.ID, &fromNote, &fromSource, &toNote, &toSource, &kind, &ctx); err != nil {
			return nil, err
		}
		if fromNote.Valid {
			v := fromNote.Int64
			l.FromNoteID = &v
		}
		if fromSource.Valid {
			v := fromSource.Int64
			l.FromSourceID = &v
		}
		if toNote.Valid {
			v := toNote.Int64
			l.ToNoteID = &v
		}
		if toSource.Valid {
			v := toSource.Int64
			l.ToSourceID = &v
		}
		l.Kind = LinkKind(kind.String)
		l.Context = ctx.String
		out = append(out, &l)
	}
	return out, rows.Err()
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStringJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
