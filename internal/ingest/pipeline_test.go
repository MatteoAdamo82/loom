package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// erroringLLM always returns the configured error.
type erroringLLM struct{ err error }

func (e *erroringLLM) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, e.err
}
func (*erroringLLM) Name() string { return "erroring-llm" }

type fakeLLM struct {
	responses []string
	calls     int
}

func (f *fakeLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.calls >= len(f.responses) {
		return &llm.ChatResponse{Content: f.responses[len(f.responses)-1]}, nil
	}
	r := f.responses[f.calls]
	f.calls++
	return &llm.ChatResponse{Content: r}, nil
}

func (f *fakeLLM) Name() string { return "fake-llm" }

func newPipelineTest(t *testing.T, responses ...string) (*Pipeline, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p := NewPipeline(s, &fakeLLM{responses: responses})
	return p, s
}

const analysisJSON = `{
  "title": "LLM Wiki pattern",
  "summary": "Karpathy proposes a pattern where the LLM maintains a personal markdown wiki, updating pages and backlinks on ingest.",
  "keywords": ["llm", "wiki", "knowledge-base", "karpathy"],
  "entities": [
    {"name": "Andrej Karpathy", "kind": "person"},
    {"name": "Obsidian",       "kind": "product"},
    {"name": "Memex",          "kind": "concept"}
  ]
}`

func writeDoc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIngestCreatesSourceNotesAndLinks(t *testing.T) {
	p, s := newPipelineTest(t, analysisJSON)
	ctx := context.Background()

	body := "# LLM Wiki\n\nThe LLM Wiki idea is about Karpathy's Memex-style notes in Obsidian.\n\nSecond paragraph talks about scaling limits.\n"
	path := writeDoc(t, t.TempDir(), "wiki.md", body)

	res, err := p.Ingest(ctx, path)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.Deduplicated {
		t.Fatal("first ingest should not be a dedup")
	}
	if res.Source == nil || res.Source.ID == 0 {
		t.Fatal("source not persisted")
	}
	if res.ChunksCreated < 1 {
		t.Errorf("expected at least 1 chunk, got %d", res.ChunksCreated)
	}
	// 1 summary + 3 entities = 4 notes
	if got := len(res.NotesCreated); got != 4 {
		t.Errorf("notes created = %d, want 4 (summary + 3 entities)", got)
	}
	if res.EntitiesLinked != 3 {
		t.Errorf("entities linked = %d, want 3", res.EntitiesLinked)
	}

	// Summary note exists and is linked to the source.
	summary, err := s.GetNoteBySlug(ctx, "llm-wiki-pattern-summary")
	if err != nil {
		t.Fatalf("summary note lookup: %v", err)
	}
	out, err := s.LinksFromNote(ctx, summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toSource, wikilinks int
	for _, l := range out {
		if l.ToSourceID != nil && l.Kind == storage.LinkDerivedFrom {
			toSource++
		}
		if l.ToNoteID != nil && l.Kind == storage.LinkWikilink {
			wikilinks++
		}
	}
	if toSource != 1 {
		t.Errorf("derived-from source links = %d, want 1", toSource)
	}
	if wikilinks != 3 {
		t.Errorf("wikilinks from summary = %d, want 3", wikilinks)
	}

	// Entity stub exists.
	entity, err := s.GetNoteBySlug(ctx, "andrej-karpathy")
	if err != nil {
		t.Fatalf("entity lookup: %v", err)
	}
	if entity.Kind != "entity" {
		t.Errorf("entity kind = %q", entity.Kind)
	}

	// FTS index includes the new notes.
	hits, err := s.Search(ctx, "karpathy", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Error("expected search hit for karpathy after ingest")
	}

	// Operations log has one ingest entry.
	var ops int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM operations WHERE kind = 'ingest'`,
	).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 1 {
		t.Errorf("ingest operations logged = %d, want 1", ops)
	}
}

func TestIngestDedupByHash(t *testing.T) {
	p, _ := newPipelineTest(t, analysisJSON)
	ctx := context.Background()

	body := "# Same Doc\n\nSame body, padded to clear the empty-doc guard threshold."
	path := writeDoc(t, t.TempDir(), "same.md", body)

	if _, err := p.Ingest(ctx, path); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	res, err := p.Ingest(ctx, path)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if !res.Deduplicated {
		t.Error("second ingest of same content should be a dedup no-op")
	}
	if res.ChunksCreated != 0 || len(res.NotesCreated) != 0 {
		t.Errorf("dedup should not create anything: %+v", res)
	}
}

func TestIngestEntityReusedAcrossSources(t *testing.T) {
	p, s := newPipelineTest(t, analysisJSON, analysisJSON)
	ctx := context.Background()

	first := writeDoc(t, t.TempDir(), "a.md", "# A\n\nfirst article about Karpathy and Obsidian.")
	second := writeDoc(t, t.TempDir(), "b.md", "# B\n\ncompletely different body about Karpathy and Obsidian plus Memex.")

	if _, err := p.Ingest(ctx, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := p.Ingest(ctx, second); err != nil {
		t.Fatalf("second: %v", err)
	}

	// Andrej Karpathy note should have two citations (one per source).
	entity, err := s.GetNoteBySlug(ctx, "andrej-karpathy")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	out, err := s.LinksFromNote(ctx, entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	var citations int
	for _, l := range out {
		if l.Kind == storage.LinkCitation {
			citations++
		}
	}
	if citations != 2 {
		t.Errorf("citations after two ingests = %d, want 2", citations)
	}
}

func TestIngestAnalyzeFailureLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	boom := errors.New("simulated analyze timeout")
	p := NewPipeline(s, &erroringLLM{err: boom})
	ctx := context.Background()
	path := writeDoc(t, t.TempDir(), "doc.md", "# Doc\n\nbody text\n")

	_, err = p.Ingest(ctx, path)
	if err == nil {
		t.Fatal("expected analyze failure to propagate")
	}
	if !strings.Contains(err.Error(), "analyze") {
		t.Errorf("error should mention analyze step: %v", err)
	}

	// The DB must be completely untouched after an analyze failure so a later
	// retry can succeed.
	assertCount(t, s, "sources", 0)
	assertCount(t, s, "chunks", 0)
	assertCount(t, s, "notes", 0)
	assertCount(t, s, "links", 0)
	assertCount(t, s, "operations", 0)
}

func TestIngestRetryAfterAnalyzeFailureSucceeds(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	path := writeDoc(t, t.TempDir(), "doc.md", "# LLM Wiki\n\nbody mentions Karpathy and the wiki maintenance pattern.\n")
	ctx := context.Background()

	// First attempt: analyze fails.
	p1 := NewPipeline(s, &erroringLLM{err: errors.New("transient")})
	if _, err := p1.Ingest(ctx, path); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	assertCount(t, s, "sources", 0)

	// Second attempt with a working LLM: should fully ingest (not dedup).
	p2 := NewPipeline(s, &fakeLLM{responses: []string{analysisJSON}})
	res, err := p2.Ingest(ctx, path)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.Deduplicated {
		t.Error("retry should not be treated as dedup since prior attempt wrote nothing")
	}
	if len(res.NotesCreated) != 4 {
		t.Errorf("notes created after retry = %d, want 4", len(res.NotesCreated))
	}
}

func assertCount(t *testing.T, s *storage.Store, table string, want int) {
	t.Helper()
	var got int
	// Table name is test-controlled; no SQL injection risk.
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("%s count = %d, want %d", table, got, want)
	}
}

func TestAnalyzeHandlesCodeFence(t *testing.T) {
	fake := &fakeLLM{responses: []string{"```json\n" + analysisJSON + "\n```"}}
	res, err := Analyze(context.Background(), fake, "doc text padded above the empty-doc guard threshold for tests.")
	if err != nil {
		t.Fatalf("analyze with code fence: %v", err)
	}
	if res.Title != "LLM Wiki pattern" {
		t.Errorf("unexpected title: %q", res.Title)
	}
	if len(res.Entities) != 3 {
		t.Errorf("entities = %d, want 3", len(res.Entities))
	}
}

func TestAnalyzeRejectsEmptyDocument(t *testing.T) {
	fake := &fakeLLM{responses: []string{analysisJSON}}
	_, err := Analyze(context.Background(), fake, "  ")
	if !errors.Is(err, ErrEmptyDocument) {
		t.Errorf("want ErrEmptyDocument for whitespace input, got %v", err)
	}
	if fake.calls > 0 {
		t.Errorf("LLM should not have been called for empty input, got %d calls", fake.calls)
	}
}

func TestAnalyzeRetriesOnInvalidJSON(t *testing.T) {
	// First response is conversational prose (the bug we're guarding against);
	// retry path returns valid JSON.
	fake := &fakeLLM{responses: []string{
		"Please provide the source document you would like me to analyze.",
		analysisJSON,
	}}
	res, err := Analyze(context.Background(), fake,
		"# Some doc\n\nLong enough body text to clear the empty-doc guard for the test.")
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if res.Title != "LLM Wiki pattern" {
		t.Errorf("title from retry response missing: %q", res.Title)
	}
	if fake.calls != 2 {
		t.Errorf("expected 2 LLM calls (initial + retry), got %d", fake.calls)
	}
}

func TestAnalyzeStillFailsAfterRetry(t *testing.T) {
	fake := &fakeLLM{responses: []string{
		"first prose reply",
		"second prose reply, also not JSON",
	}}
	_, err := Analyze(context.Background(), fake,
		"# Doc\n\nLong enough body text to clear the empty-doc guard for the test.")
	if err == nil {
		t.Fatal("expected error after both attempts return prose")
	}
	if !strings.Contains(err.Error(), "parse analyze output") {
		t.Errorf("error should retain original parse context: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Andrej Karpathy":         "andrej-karpathy",
		"Città di Milano":         "citta-di-milano",
		"  Héllo, World!!  ":      "hello-world",
		"Multi   Spaces___Here":   "multi-spaces-here",
		"":                        "untitled",
		"---":                     "untitled",
	}
	for in, want := range cases {
		got := Slugify(in)
		if got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
