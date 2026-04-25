// Package lint inspects a Loom store for hygiene issues:
// orphan notes, near-duplicate notes, and entity gaps.
//
// MVP scope: deterministic SQL-only checks. The richer LLM-driven contradiction
// detection is left for a later pass.
package lint

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Severity ranks findings so the CLI can colour or filter them.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Kind     string // orphan | duplicate | gap | stale
	Severity Severity
	Subject  string  // e.g. note slug, entity name, source title
	Message  string  // human-readable summary
	Refs     []string // related entity refs ("note:1", "source:7", ...)
}

type Report struct {
	Findings []Finding
	// Stats are returned alongside findings so the caller can show coverage
	// even when nothing is wrong.
	Stats Stats
}

type Stats struct {
	Notes        int
	Sources      int
	Entities     int
	OrphanNotes  int
	Duplicates   int
	Gaps         int
}

type Config struct {
	// MinKeywordOverlap is the Jaccard threshold (0..1) above which two notes
	// of the same kind are flagged as near-duplicates. Default: 0.6.
	MinKeywordOverlap float64
	// MinKeywords gates the duplicate check on sample size: pairs where
	// either side has fewer than this many keywords are skipped. Without
	// this, ingest-time entity stubs (which carry a single kind keyword
	// like "person") all collide at 100% Jaccard and drown the report.
	// Default: 3.
	MinKeywords int
	// IgnoreKinds lists note kinds excluded from orphan detection (summary
	// notes are intentionally not backlinked from elsewhere). Default:
	// {"summary"}.
	IgnoreKinds []string
}

func (c Config) withDefaults() Config {
	if c.MinKeywordOverlap == 0 {
		c.MinKeywordOverlap = 0.6
	}
	if c.MinKeywords == 0 {
		c.MinKeywords = 3
	}
	if c.IgnoreKinds == nil {
		c.IgnoreKinds = []string{"summary"}
	}
	return c
}

// Run executes every check against the given store.
func Run(ctx context.Context, store *storage.Store, cfg Config) (*Report, error) {
	cfg = cfg.withDefaults()
	report := &Report{}

	if err := collectStats(ctx, store, &report.Stats); err != nil {
		return nil, err
	}

	if err := checkOrphans(ctx, store, cfg, report); err != nil {
		return nil, err
	}
	if err := checkDuplicates(ctx, store, cfg, report); err != nil {
		return nil, err
	}
	if err := checkGaps(ctx, store, report); err != nil {
		return nil, err
	}

	return report, nil
}

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

func collectStats(ctx context.Context, store *storage.Store, s *Stats) error {
	queries := map[string]*int{
		`SELECT COUNT(*) FROM notes`:                          &s.Notes,
		`SELECT COUNT(*) FROM sources`:                        &s.Sources,
		`SELECT COUNT(*) FROM notes WHERE kind = 'entity'`:    &s.Entities,
	}
	for q, dst := range queries {
		if err := store.DB().QueryRowContext(ctx, q).Scan(dst); err != nil {
			return fmt.Errorf("stats query %q: %w", q, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// orphans: notes nobody links to
// ---------------------------------------------------------------------------

func checkOrphans(ctx context.Context, store *storage.Store, cfg Config, report *Report) error {
	excluded := strings.Join(quoteAll(cfg.IgnoreKinds), ",")
	if excluded == "" {
		excluded = "''"
	}
	q := fmt.Sprintf(`
		SELECT n.id, n.slug, n.title, n.kind
		  FROM notes n
		 WHERE n.kind NOT IN (%s)
		   AND NOT EXISTS (SELECT 1 FROM links l WHERE l.to_note_id = n.id)
		 ORDER BY n.id`, excluded)

	rows, err := store.DB().QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("orphans query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var slug, title, kind string
		if err := rows.Scan(&id, &slug, &title, &kind); err != nil {
			return err
		}
		report.Stats.OrphanNotes++
		report.Findings = append(report.Findings, Finding{
			Kind:     "orphan",
			Severity: SeverityInfo,
			Subject:  slug,
			Message:  fmt.Sprintf("%s note %q has no inbound links", kind, title),
			Refs:     []string{fmt.Sprintf("note:%d", id)},
		})
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// duplicates: notes of the same kind sharing a high keyword overlap
// ---------------------------------------------------------------------------

type noteRow struct {
	ID       int64
	Slug     string
	Title    string
	Kind     string
	Keywords []string
}

func checkDuplicates(ctx context.Context, store *storage.Store, cfg Config, report *Report) error {
	notes, err := loadNoteRows(ctx, store)
	if err != nil {
		return err
	}
	// Group by kind to keep comparisons local.
	byKind := map[string][]*noteRow{}
	for _, n := range notes {
		byKind[n.Kind] = append(byKind[n.Kind], n)
	}

	for kind, group := range byKind {
		if kind == "summary" {
			// summary notes are 1-per-source by construction; near-misses
			// there usually mean two articles cover the same topic, which is
			// a content question rather than a hygiene issue.
			continue
		}
		for i := 0; i < len(group); i++ {
			if len(group[i].Keywords) < cfg.MinKeywords {
				continue
			}
			for j := i + 1; j < len(group); j++ {
				if len(group[j].Keywords) < cfg.MinKeywords {
					continue
				}
				score := jaccard(group[i].Keywords, group[j].Keywords)
				if score < cfg.MinKeywordOverlap {
					continue
				}
				report.Stats.Duplicates++
				report.Findings = append(report.Findings, Finding{
					Kind:     "duplicate",
					Severity: SeverityWarning,
					Subject:  group[i].Slug + " ~ " + group[j].Slug,
					Message: fmt.Sprintf(
						"%s notes %q and %q share %.0f%% of keywords",
						kind, group[i].Title, group[j].Title, score*100,
					),
					Refs: []string{
						fmt.Sprintf("note:%d", group[i].ID),
						fmt.Sprintf("note:%d", group[j].ID),
					},
				})
			}
		}
	}
	return nil
}

func loadNoteRows(ctx context.Context, store *storage.Store) ([]*noteRow, error) {
	rows, err := store.DB().QueryContext(ctx,
		`SELECT id, slug, title, kind, keywords FROM notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*noteRow
	for rows.Next() {
		var n noteRow
		var kw sql.NullString
		if err := rows.Scan(&n.ID, &n.Slug, &n.Title, &n.Kind, &kw); err != nil {
			return nil, err
		}
		n.Keywords = parseKeywords(kw.String)
		out = append(out, &n)
	}
	return out, rows.Err()
}

func parseKeywords(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" {
		return nil
	}
	// keywords are stored as JSON arrays; do a quick manual parse to avoid
	// importing encoding/json just for this.
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := map[string]struct{}{}
	for _, x := range a {
		setA[x] = struct{}{}
	}
	setB := map[string]struct{}{}
	for _, x := range b {
		setB[x] = struct{}{}
	}
	inter := 0
	for k := range setA {
		if _, ok := setB[k]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ---------------------------------------------------------------------------
// gaps: source titles that don't have any matching note
// ---------------------------------------------------------------------------

func checkGaps(ctx context.Context, store *storage.Store, report *Report) error {
	rows, err := store.DB().QueryContext(ctx, `
		SELECT s.id, s.title
		  FROM sources s
		 WHERE NOT EXISTS (SELECT 1 FROM links l WHERE l.to_source_id = s.id)
		 ORDER BY s.id`)
	if err != nil {
		return fmt.Errorf("gaps query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return err
		}
		report.Stats.Gaps++
		report.Findings = append(report.Findings, Finding{
			Kind:     "gap",
			Severity: SeverityWarning,
			Subject:  fmt.Sprintf("source:%d", id),
			Message:  fmt.Sprintf("source %q has no notes deriving from it", title),
			Refs:     []string{fmt.Sprintf("source:%d", id)},
		})
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// SortFindings returns a stable order: warnings first, then by kind+subject.
func SortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Severity != f[j].Severity {
			return f[i].Severity == SeverityWarning
		}
		if f[i].Kind != f[j].Kind {
			return f[i].Kind < f[j].Kind
		}
		return f[i].Subject < f[j].Subject
	})
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return out
}
