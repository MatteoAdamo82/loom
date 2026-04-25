package lint

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/storage"
)

func newStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "lint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustNote(t *testing.T, s *storage.Store, n *storage.Note) *storage.Note {
	t.Helper()
	if err := s.CreateNote(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunFindsOrphans(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	linked := mustNote(t, s, &storage.Note{Slug: "linked", Title: "L", Kind: "entity", Content: "x"})
	orphan := mustNote(t, s, &storage.Note{Slug: "orphan", Title: "O", Kind: "entity", Content: "y"})
	summary := mustNote(t, s, &storage.Note{Slug: "summary-x", Title: "S", Kind: "summary", Content: "z"})

	// Inbound link to "linked" so it isn't reported.
	if err := s.CreateLink(ctx, &storage.Link{
		FromNoteID: &summary.ID, ToNoteID: &linked.ID,
		Kind: storage.LinkWikilink, Context: "test",
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(ctx, s, Config{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var orphans []Finding
	for _, f := range report.Findings {
		if f.Kind == "orphan" {
			orphans = append(orphans, f)
		}
	}
	if len(orphans) != 1 {
		t.Fatalf("orphan count = %d, want 1, findings=%+v", len(orphans), report.Findings)
	}
	if orphans[0].Subject != orphan.Slug {
		t.Errorf("orphan subject = %q, want %q", orphans[0].Subject, orphan.Slug)
	}
	// summary was excluded
	for _, f := range orphans {
		if f.Subject == summary.Slug {
			t.Error("summary kind should be excluded from orphan check")
		}
	}
}

func TestRunFindsKeywordDuplicates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	mustNote(t, s, &storage.Note{
		Slug: "rag-1", Title: "RAG basics", Kind: "concept",
		Keywords: []string{"rag", "vector", "embedding", "retrieval"}, Content: "x",
	})
	mustNote(t, s, &storage.Note{
		Slug: "rag-2", Title: "RAG overview", Kind: "concept",
		Keywords: []string{"rag", "vector", "embedding", "retrieval"}, Content: "y",
	})
	mustNote(t, s, &storage.Note{
		Slug: "unrelated", Title: "Cats", Kind: "concept",
		Keywords: []string{"cats", "pets"}, Content: "z",
	})

	report, err := Run(ctx, s, Config{MinKeywordOverlap: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	dup := 0
	for _, f := range report.Findings {
		if f.Kind == "duplicate" {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("duplicate count = %d, want 1: %+v", dup, report.Findings)
	}
}

func TestRunFindsSourceGaps(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	gapped := &storage.Source{URI: "file:///gap.md", Kind: "md", Title: "Gap", Content: "x", Hash: "h-gap"}
	if err := s.CreateSource(ctx, gapped); err != nil {
		t.Fatal(err)
	}
	covered := &storage.Source{URI: "file:///covered.md", Kind: "md", Title: "Covered", Content: "y", Hash: "h-cov"}
	if err := s.CreateSource(ctx, covered); err != nil {
		t.Fatal(err)
	}
	note := mustNote(t, s, &storage.Note{Slug: "n", Title: "n", Kind: "summary", Content: "n"})
	if err := s.CreateLink(ctx, &storage.Link{
		FromNoteID: &note.ID, ToSourceID: &covered.ID,
		Kind: storage.LinkDerivedFrom, Context: "test",
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(ctx, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var gaps []Finding
	for _, f := range report.Findings {
		if f.Kind == "gap" {
			gaps = append(gaps, f)
		}
	}
	if len(gaps) != 1 {
		t.Fatalf("gap count = %d, want 1, findings=%+v", len(gaps), report.Findings)
	}
	if !strings.Contains(gaps[0].Message, "Gap") {
		t.Errorf("gap message should mention source title: %q", gaps[0].Message)
	}
}

func TestDuplicatesIgnoresStubEntities(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Three entity stubs, each with a single "kind" keyword — exactly what
	// the ingest pipeline emits when it discovers new entities. Before the
	// MinKeywords gate they collided pairwise at 100% Jaccard and produced
	// O(n²) noise findings.
	mustNote(t, s, &storage.Note{
		Slug: "karpathy", Title: "Andrej Karpathy", Kind: "entity",
		Keywords: []string{"person"}, Content: "stub",
	})
	mustNote(t, s, &storage.Note{
		Slug: "bush", Title: "Vannevar Bush", Kind: "entity",
		Keywords: []string{"person"}, Content: "stub",
	})
	mustNote(t, s, &storage.Note{
		Slug: "memex", Title: "Memex", Kind: "entity",
		Keywords: []string{"concept"}, Content: "stub",
	})

	report, err := Run(ctx, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range report.Findings {
		if f.Kind == "duplicate" {
			t.Errorf("stub entities (1 keyword each) should not be flagged: %+v", f)
		}
	}
}

func TestDuplicatesHonoursCustomMinKeywords(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Two identical 2-keyword pairs.
	mustNote(t, s, &storage.Note{
		Slug: "n1", Title: "N1", Kind: "concept",
		Keywords: []string{"alpha", "beta"}, Content: "x",
	})
	mustNote(t, s, &storage.Note{
		Slug: "n2", Title: "N2", Kind: "concept",
		Keywords: []string{"alpha", "beta"}, Content: "y",
	})

	// Default config (MinKeywords=3) → not flagged.
	report, err := Run(ctx, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range report.Findings {
		if f.Kind == "duplicate" {
			t.Errorf("MinKeywords=3 should skip 2-keyword pairs, got %+v", f)
		}
	}

	// Lowered explicitly → flagged.
	report, err = Run(ctx, s, Config{MinKeywords: 2})
	if err != nil {
		t.Fatal(err)
	}
	dups := 0
	for _, f := range report.Findings {
		if f.Kind == "duplicate" {
			dups++
		}
	}
	if dups != 1 {
		t.Errorf("MinKeywords=2 should flag the pair, got %d duplicates", dups)
	}
}

func TestStatsPopulated(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustNote(t, s, &storage.Note{Slug: "a", Title: "A", Kind: "entity", Content: "x"})
	mustNote(t, s, &storage.Note{Slug: "b", Title: "B", Kind: "concept", Content: "x"})

	report, err := Run(ctx, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.Notes != 2 {
		t.Errorf("notes stat = %d", report.Stats.Notes)
	}
	if report.Stats.Entities != 1 {
		t.Errorf("entity stat = %d", report.Stats.Entities)
	}
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		a, b []string
		want float64
	}{
		{[]string{"a", "b"}, []string{"a", "b"}, 1.0},
		{[]string{"a"}, []string{"b"}, 0.0},
		{[]string{"a", "b", "c"}, []string{"b", "c", "d"}, 0.5},
	}
	for _, c := range cases {
		got := jaccard(c.a, c.b)
		if got != c.want {
			t.Errorf("jaccard(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSortFindingsPutsWarningsFirst(t *testing.T) {
	in := []Finding{
		{Kind: "orphan", Severity: SeverityInfo, Subject: "z"},
		{Kind: "duplicate", Severity: SeverityWarning, Subject: "a"},
		{Kind: "gap", Severity: SeverityWarning, Subject: "m"},
	}
	SortFindings(in)
	if in[0].Severity != SeverityWarning || in[1].Severity != SeverityWarning {
		t.Errorf("warnings should sort first: %+v", in)
	}
	if in[2].Severity != SeverityInfo {
		t.Errorf("info should sort last: %+v", in)
	}
}
