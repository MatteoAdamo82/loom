package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotent.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer s2.Close()

	var v int
	err = s2.DB().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if v != currentSchemaVersion {
		t.Errorf("schema version = %d, want %d", v, currentSchemaVersion)
	}
}

func TestSourceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	src := &Source{
		URI:     "file:///tmp/article.md",
		Kind:    "md",
		Title:   "Article",
		Content: "hello world",
		Hash:    "abc123",
	}
	if err := s.CreateSource(ctx, src); err != nil {
		t.Fatalf("create: %v", err)
	}
	if src.ID == 0 {
		t.Fatal("expected id to be set")
	}

	got, err := s.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URI != src.URI || got.Content != src.Content {
		t.Errorf("round-trip mismatch: got %+v", got)
	}

	byHash, err := s.GetSourceByHash(ctx, "abc123")
	if err != nil {
		t.Fatalf("get by hash: %v", err)
	}
	if byHash.ID != src.ID {
		t.Errorf("hash lookup id = %d, want %d", byHash.ID, src.ID)
	}

	if _, err := s.GetSourceByHash(ctx, "missing"); err != ErrNotFound {
		t.Errorf("missing hash: want ErrNotFound, got %v", err)
	}
}

func TestNoteLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n := &Note{
		Slug:     "andrej-karpathy",
		Title:    "Andrej Karpathy",
		Kind:     "entity",
		Content:  "Former director of AI at Tesla.",
		Summary:  "AI researcher and educator.",
		Keywords: []string{"ai", "tesla", "openai", "education"},
	}
	if err := s.CreateNote(ctx, n); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if n.Version != 1 {
		t.Errorf("new note version = %d, want 1", n.Version)
	}

	got, err := s.GetNoteBySlug(ctx, "andrej-karpathy")
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if len(got.Keywords) != 4 || got.Keywords[0] != "ai" {
		t.Errorf("keywords not round-tripped: %v", got.Keywords)
	}

	got.Content = "Updated bio."
	got.Keywords = append(got.Keywords, "llm")
	if err := s.UpdateNote(ctx, got, "ingest of new article"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("updated version = %d, want 2", got.Version)
	}

	var archived int
	err = s.DB().QueryRow(
		`SELECT COUNT(*) FROM note_versions WHERE note_id = ?`, got.ID,
	).Scan(&archived)
	if err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if archived != 1 {
		t.Errorf("archived versions = %d, want 1", archived)
	}
}

func TestLinksCheckConstraint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	note := &Note{Slug: "a", Title: "A", Kind: "entity", Content: "x"}
	if err := s.CreateNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	other := &Note{Slug: "b", Title: "B", Kind: "entity", Content: "y"}
	if err := s.CreateNote(ctx, other); err != nil {
		t.Fatal(err)
	}

	l := &Link{
		FromNoteID: &note.ID,
		ToNoteID:   &other.ID,
		Kind:       LinkWikilink,
		Context:    "A mentions [[b]]",
	}
	if err := s.CreateLink(ctx, l); err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Invalid: both from endpoints set.
	bad := &Link{
		FromNoteID:   &note.ID,
		FromSourceID: &note.ID,
		ToNoteID:     &other.ID,
		Kind:         LinkSeeAlso,
	}
	if err := s.CreateLink(ctx, bad); err == nil {
		t.Error("expected CHECK constraint to reject both-from endpoints")
	}

	out, err := s.LinksFromNote(ctx, note.ID)
	if err != nil {
		t.Fatalf("list from: %v", err)
	}
	if len(out) != 1 || *out[0].ToNoteID != other.ID {
		t.Errorf("unexpected links: %+v", out)
	}

	backs, err := s.LinksToNote(ctx, other.ID)
	if err != nil {
		t.Fatalf("list to: %v", err)
	}
	if len(backs) != 1 {
		t.Errorf("backlinks = %d, want 1", len(backs))
	}
}

func TestSearchBM25(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	notes := []*Note{
		{
			Slug: "karpathy", Title: "Andrej Karpathy", Kind: "entity",
			Content:  "Former AI director at Tesla, built the autopilot team.",
			Summary:  "AI researcher.",
			Keywords: []string{"ai", "tesla", "autopilot"},
		},
		{
			Slug: "llm-wiki", Title: "LLM Wiki pattern", Kind: "concept",
			Content:  "A pattern where the LLM maintains a personal knowledge base in markdown.",
			Summary:  "LLM-maintained wiki.",
			Keywords: []string{"llm", "wiki", "knowledge-base"},
		},
		{
			Slug: "rag", Title: "Retrieval Augmented Generation", Kind: "concept",
			Content:  "Vector databases and embeddings support semantic retrieval for LLMs.",
			Summary:  "RAG overview.",
			Keywords: []string{"rag", "vector", "embedding"},
		},
	}
	for _, n := range notes {
		if err := s.CreateNote(ctx, n); err != nil {
			t.Fatalf("create %s: %v", n.Slug, err)
		}
		if err := s.IndexNote(ctx, n); err != nil {
			t.Fatalf("index %s: %v", n.Slug, err)
		}
	}

	hits, err := s.Search(ctx, "karpathy autopilot", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for karpathy query")
	}
	if !strings.HasPrefix(hits[0].EntityRef, "note:") {
		t.Errorf("first hit ref = %q", hits[0].EntityRef)
	}

	hits, err = s.Search(ctx, "wiki", 10)
	if err != nil {
		t.Fatalf("search wiki: %v", err)
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Title, "LLM Wiki") {
			found = true
		}
	}
	if !found {
		t.Errorf("wiki search did not return LLM Wiki note: %+v", hits)
	}
}

func TestIndexNoteRemovesOldRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n := &Note{Slug: "x", Title: "original title", Kind: "entity", Content: "original body"}
	if err := s.CreateNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexNote(ctx, n); err != nil {
		t.Fatal(err)
	}

	n.Title = "renamed title"
	n.Content = "updated body quartz"
	if err := s.UpdateNote(ctx, n, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.IndexNote(ctx, n); err != nil {
		t.Fatal(err)
	}

	var rows int
	err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM search_index WHERE entity_ref = ?`, "note:1",
	).Scan(&rows)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("expected exactly 1 indexed row, got %d", rows)
	}

	hits, err := s.Search(ctx, "quartz", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit after reindex, got %d", len(hits))
	}
	hits, err = s.Search(ctx, "original", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("stale term should not match, got %+v", hits)
	}
}

func TestLogOperation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	op := &Operation{
		Kind:    "ingest",
		Actor:   "llama3.1:8b",
		Summary: "Ingested article.md",
		Details: []byte(`{"sources":1,"notes_touched":3}`),
	}
	if err := s.LogOperation(ctx, op); err != nil {
		t.Fatalf("log: %v", err)
	}
	if op.ID == 0 {
		t.Error("expected id to be set")
	}

	var count int
	err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM operations WHERE kind = 'ingest'`,
	).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("operations = %d, want 1", count)
	}
}
