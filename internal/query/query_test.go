package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type scriptedLLM struct {
	responses []string
	calls     int
}

func (s *scriptedLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if s.calls >= len(s.responses) {
		return &llm.ChatResponse{Content: s.responses[len(s.responses)-1]}, nil
	}
	r := s.responses[s.calls]
	s.calls++
	return &llm.ChatResponse{Content: r}, nil
}

func (s *scriptedLLM) Name() string { return "scripted" }

func seedStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	notes := []*storage.Note{
		{Slug: "karpathy", Title: "Andrej Karpathy", Kind: "entity",
			Content:  "Former director of AI at Tesla, built the autopilot team; now independent.",
			Summary:  "AI researcher formerly at OpenAI and Tesla.",
			Keywords: []string{"ai", "tesla", "autopilot", "openai"}},
		{Slug: "llm-wiki", Title: "LLM Wiki pattern", Kind: "concept",
			Content:  "A pattern where the LLM maintains a personal markdown wiki and updates backlinks.",
			Summary:  "LLM-maintained wiki as alternative to RAG.",
			Keywords: []string{"llm", "wiki", "knowledge-base"}},
		{Slug: "rag", Title: "Retrieval Augmented Generation", Kind: "concept",
			Content:  "Vector databases and embeddings support semantic retrieval for LLMs.",
			Summary:  "RAG overview.",
			Keywords: []string{"rag", "vector", "embedding"}},
	}
	for _, n := range notes {
		if err := s.CreateNote(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := s.IndexNote(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	// Link karpathy -> llm-wiki so graph boost has something to follow.
	linkNotes(t, s, "karpathy", "llm-wiki")
	return s
}

func linkNotes(t *testing.T, s *storage.Store, fromSlug, toSlug string) {
	t.Helper()
	ctx := context.Background()
	from, err := s.GetNoteBySlug(ctx, fromSlug)
	if err != nil {
		t.Fatal(err)
	}
	to, err := s.GetNoteBySlug(ctx, toSlug)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLink(ctx, &storage.Link{
		FromNoteID: &from.ID, ToNoteID: &to.ID,
		Kind: storage.LinkWikilink, Context: "test-seeded",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExpandParsesJSON(t *testing.T) {
	fake := &scriptedLLM{responses: []string{
		`{"queries":["karpathy tesla","autopilot team","openai cofounder","karpathy tesla","   "]}`,
	}}
	qs, err := Expand(context.Background(), fake, "Who founded Tesla's autopilot team?")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(qs) != 3 {
		t.Errorf("got %d queries after dedup, want 3: %v", len(qs), qs)
	}
}

func TestRerankPreservesOnlyKnownIDs(t *testing.T) {
	cands := []Candidate{
		{EntityRef: "note:1", Title: "A"},
		{EntityRef: "note:2", Title: "B"},
		{EntityRef: "note:3", Title: "C"},
	}
	fake := &scriptedLLM{responses: []string{
		`{"ranked":["note:3","note:9","note:1"]}`,
	}}
	out, err := Rerank(context.Background(), fake, "q", cands, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 known ids, got %d: %+v", len(out), out)
	}
	if out[0].EntityRef != "note:3" || out[1].EntityRef != "note:1" {
		t.Errorf("unexpected rerank order: %+v", out)
	}
}

func TestEngineEndToEnd(t *testing.T) {
	s := seedStore(t)

	// Expand → BM25 search → (graph boost adds llm-wiki) → rerank → synthesize
	fake := &scriptedLLM{responses: []string{
		`{"queries":["karpathy autopilot","andrej karpathy","tesla ai"]}`,
		`{"ranked":["note:1","note:2"]}`, // note:1 is karpathy, note:2 is llm-wiki
		"Karpathy led Tesla's autopilot team. [note:1]\n\nSources: [note:1]",
	}}

	eng := NewEngine(s, fake)
	ans, err := eng.Run(context.Background(), "Who led Tesla's autopilot team?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(ans.Content, "Karpathy") {
		t.Errorf("answer missing expected entity: %q", ans.Content)
	}
	if len(ans.Candidates) == 0 {
		t.Error("expected rerank candidates")
	}
	if len(ans.Citations) == 0 {
		t.Error("expected citations")
	}

	// operations log has a query entry
	var c int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM operations WHERE kind='query'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Errorf("query ops = %d, want 1", c)
	}
}

func TestEngineEmptyResult(t *testing.T) {
	s := seedStore(t)
	fake := &scriptedLLM{responses: []string{
		`{"queries":["zzzzz not in index qwerty"]}`,
		`{"ranked":[]}`,
		"Should not reach synthesize.",
	}}
	eng := NewEngine(s, fake)
	ans, err := eng.Run(context.Background(), "random unrelated question about qwerty")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Content, "No relevant") {
		t.Errorf("unexpected answer for empty index: %q", ans.Content)
	}
}

func TestSanitizeFTSQuery(t *testing.T) {
	cases := map[string]string{
		"hello world":    `"hello" OR "world"`,
		"what's up?":     `"what's" OR "up"`,
		"":               "",
		"(malicious *)":  `"malicious"`,
	}
	for in, want := range cases {
		got := sanitizeFTSQuery(in)
		if got != want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
