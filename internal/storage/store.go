// Package storage owns the SQLite index. The filesystem holds the truth (one
// folder of files); the DB just speeds up lookups and keeps LLM-generated
// summaries+keywords next to each file.
package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
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

// ---------------------------------------------------------------------------
// Vectors — optional hybrid (BM25 + semantic) search.
//
// Vectors live in a separate table (file_vectors) so the BM25 path is byte-for
// byte unchanged when embeddings are disabled. Search is pure-Go brute-force
// cosine: at Loom's scale (hundreds–thousands of files) this is sub-millisecond
// and keeps the build CGO-free (no sqlite-vec extension).
// ---------------------------------------------------------------------------

// rrfK is the Reciprocal Rank Fusion constant. 60 is the value from the
// original RRF paper and the de-facto default; it dampens the contribution of
// low-ranked items without needing to normalise BM25 and cosine scores.
const rrfK = 60

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func l2norm(v []float32) float64 {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	return math.Sqrt(s)
}

// cosine returns the cosine similarity of a and b. aNorm is passed in so the
// query vector's norm is computed once across the whole scan.
func cosine(a, b []float32, aNorm, bNorm float64) float64 {
	if aNorm == 0 || bNorm == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (aNorm * bNorm)
}

// SetVector stores (or replaces) the embedding for the file at relPath. A
// zero-length vector is a no-op. Unknown relPath also no-ops (the INSERT…SELECT
// simply matches no rows).
func (s *Store) SetVector(ctx context.Context, relPath string, vec []float32, model string) error {
	if len(vec) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO file_vectors (file_id, dim, model, vec)
		SELECT id, ?, ?, ? FROM files WHERE rel_path = ?
		ON CONFLICT(file_id) DO UPDATE SET
			dim = excluded.dim, model = excluded.model, vec = excluded.vec`,
		len(vec), model, encodeVector(vec), relPath)
	return err
}

// HasVectors reports whether any file vector exists. Callers use it to decide
// whether hybrid search is worth attempting.
func (s *Store) HasVectors(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_vectors`).Scan(&n)
	return n > 0, err
}

// VectorSearch returns up to limit files ranked by cosine similarity to vec.
// Vectors whose dimension differs from len(vec) (e.g. indexed with a different
// embedding model) are skipped, so a model change degrades gracefully rather
// than erroring — until the next `loom scan --force` re-embeds everything.
func (s *Store) VectorSearch(ctx context.Context, vec []float32, limit int) ([]*Hit, error) {
	if len(vec) == 0 {
		return nil, ErrNotFound
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.rel_path, f.hash, f.mtime, f.kind, f.title, f.content,
		       f.summary, f.keywords, f.indexed_at, v.vec
		FROM file_vectors v JOIN files f ON f.id = v.file_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	qNorm := l2norm(vec)
	var scored []*Hit
	for rows.Next() {
		var h Hit
		var keywordsJSON string
		var blob []byte
		if err := rows.Scan(
			&h.ID, &h.RelPath, &h.Hash, &h.MTime, &h.Kind, &h.Title, &h.Content,
			&h.Summary, &keywordsJSON, &h.IndexedAt, &blob,
		); err != nil {
			return nil, err
		}
		fv := decodeVector(blob)
		if len(fv) != len(vec) {
			continue // indexed with a different model/dimension — skip
		}
		_ = json.Unmarshal([]byte(keywordsJSON), &h.Keywords)
		h.Rank = cosine(vec, fv, qNorm, l2norm(fv))
		scored = append(scored, &h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Rank > scored[j].Rank })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// HybridSearch fuses BM25 and vector rankings with Reciprocal Rank Fusion and
// returns up to limit hits. It pulls a wider candidate pool from each side, then
// fuses: a file ranked high by either signal surfaces. On the returned hits,
// Rank holds the RRF score (higher is better — note this is the opposite sense
// of the bm25() rank used by Search). Returns ErrNotFound if both sides empty.
func (s *Store) HybridSearch(ctx context.Context, query string, vec []float32, limit int) ([]*Hit, error) {
	if limit <= 0 {
		limit = 5
	}
	cand := limit * 4
	if cand < 20 {
		cand = 20
	}

	bm, err := s.Search(ctx, query, cand)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	vs, err := s.VectorSearch(ctx, vec, cand)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	type agg struct {
		hit   *Hit
		score float64
	}
	by := make(map[int64]*agg)
	fuse := func(list []*Hit) {
		for rank, h := range list {
			a := by[h.ID]
			if a == nil {
				a = &agg{hit: h}
				by[h.ID] = a
			}
			a.score += 1.0 / float64(rrfK+rank+1)
		}
	}
	fuse(bm)
	fuse(vs)

	out := make([]*Hit, 0, len(by))
	for _, a := range by {
		a.hit.Rank = a.score
		out = append(out, a.hit)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank > out[j].Rank })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
