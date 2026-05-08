package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type stubLLM struct {
	lastPrompt string
	respond    func(string) string
}

func (s *stubLLM) Name() string { return "stub" }
func (s *stubLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			s.lastPrompt = m.Content
		}
	}
	resp := "Una risposta. [a.md]"
	if s.respond != nil {
		resp = s.respond(s.lastPrompt)
	}
	return &llm.ChatResponse{Content: resp}, nil
}

func newStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *storage.Store, files []storage.File) {
	t.Helper()
	for _, f := range files {
		if err := s.Upsert(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnswerHappyPath(t *testing.T) {
	s := newStore(t)
	seed(t, s, []storage.File{
		{RelPath: "a.md", Hash: "1", MTime: 1, Kind: "md", Title: "Alpha",
			Content: "alpha content", Summary: "alpha summary",
			Keywords: []string{"alpha"}},
		{RelPath: "b.md", Hash: "2", MTime: 1, Kind: "md", Title: "Beta",
			Content: "beta content", Summary: "beta summary",
			Keywords: []string{"beta"}},
	})

	lc := &stubLLM{}
	res, err := Answer(context.Background(), s, lc, "alpha", 5)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if res.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if !strings.Contains(lc.lastPrompt, "alpha summary") {
		t.Errorf("prompt should include summary of top hit; got: %q", lc.lastPrompt)
	}
	if !strings.Contains(lc.lastPrompt, "[a.md]") {
		t.Errorf("prompt should label sources with rel_path; got: %q", lc.lastPrompt)
	}
	if len(res.Citations) != 1 || res.Citations[0].RelPath != "a.md" {
		t.Errorf("expected one citation a.md, got %+v", res.Citations)
	}
}

func TestAnswerWithEmptyIndex(t *testing.T) {
	s := newStore(t)
	lc := &stubLLM{}
	res, err := Answer(context.Background(), s, lc, "anything", 5)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Answer), "nessuna nota") {
		t.Errorf("expected empty-index sentinel, got %q", res.Answer)
	}
}

func TestPerFileBudgetIsRespected(t *testing.T) {
	s := newStore(t)
	long := strings.Repeat("x ", PerFileCharBudget)
	seed(t, s, []storage.File{
		{RelPath: "a.md", Hash: "1", MTime: 1, Kind: "md", Title: "A",
			Content: long, Summary: "many xs", Keywords: []string{"xs"}},
	})

	lc := &stubLLM{}
	if _, err := Answer(context.Background(), s, lc, "xs", 5); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lc.lastPrompt, "[...truncated]") {
		t.Errorf("expected truncation marker in prompt for oversized file")
	}
}

func TestCitationParserDeduplicates(t *testing.T) {
	s := newStore(t)
	seed(t, s, []storage.File{
		{RelPath: "a.md", Hash: "1", MTime: 1, Kind: "md", Title: "Alpha",
			Content: "x", Summary: "alpha", Keywords: []string{"alpha"}},
	})
	lc := &stubLLM{respond: func(string) string {
		return "Alpha [a.md]. Anche qui [a.md]. E anche [b.md] (mancante)."
	}}
	res, err := Answer(context.Background(), s, lc, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	seen := map[string]bool{}
	for _, c := range res.Citations {
		if seen[c.RelPath] {
			t.Errorf("duplicate citation for %s", c.RelPath)
		}
		seen[c.RelPath] = true
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 unique citations (a.md and b.md), got %d", count)
	}
}
