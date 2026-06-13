package query

import (
	"context"
	"errors"
	"testing"

	"github.com/MatteoAdamo82/loom/internal/storage"
)

// stubEmbedder returns a fixed vector per input text, so the hybrid path is
// exercised without a live embedding model. Texts not in the map get a zero-ish
// fallback vector.
type stubEmbedder struct {
	byText map[string][]float32
	err    error
	calls  int
}

func (e *stubEmbedder) Name() string { return "stub-embed" }
func (e *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := e.byText[t]; ok {
			out[i] = v
		} else {
			out[i] = []float32{0.01, 0.01, 0.01}
		}
	}
	return out, nil
}

func seedWithVectors(t *testing.T, s *storage.Store) {
	t.Helper()
	seed(t, s, []storage.File{
		{RelPath: "kw.md", Hash: "1", MTime: 1, Kind: "md", Title: "Keyword",
			Content: "guanciale pecorino", Summary: "ricetta", Keywords: []string{"pasta"}},
		{RelPath: "vec.md", Hash: "2", MTime: 1, Kind: "md", Title: "Vector",
			Content: "unrelated prose", Summary: "altro", Keywords: []string{"altro"}},
	})
	if err := s.SetVector(context.Background(), "kw.md", []float32{0, 1, 0}, "m"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVector(context.Background(), "vec.md", []float32{1, 0, 0}, "m"); err != nil {
		t.Fatal(err)
	}
}

func TestRetrieveBM25WhenNoEmbedder(t *testing.T) {
	s := newStore(t)
	seedWithVectors(t, s)

	hits, err := Retrieve(context.Background(), s, nil, "guanciale", 5)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(hits) != 1 || hits[0].RelPath != "kw.md" {
		t.Errorf("BM25 should return only the keyword match, got %+v", hits)
	}
}

func TestRetrieveHybridSurfacesVectorMatch(t *testing.T) {
	s := newStore(t)
	seedWithVectors(t, s)

	// The query "guanciale" only matches kw.md via BM25, but the query vector
	// points at vec.md — hybrid should surface both.
	emb := &stubEmbedder{byText: map[string][]float32{"guanciale": {1, 0, 0}}}
	hits, err := Retrieve(context.Background(), s, emb, "guanciale", 5)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if emb.calls != 1 {
		t.Errorf("expected the query to be embedded once, got %d calls", emb.calls)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.RelPath] = true
	}
	if !seen["kw.md"] || !seen["vec.md"] {
		t.Errorf("hybrid should surface both kw.md and vec.md, got %+v", hits)
	}
}

func TestRetrieveDegradesToBM25OnEmbedError(t *testing.T) {
	s := newStore(t)
	seedWithVectors(t, s)

	emb := &stubEmbedder{err: errors.New("embed backend down")}
	hits, err := Retrieve(context.Background(), s, emb, "guanciale", 5)
	if err != nil {
		t.Fatalf("embed failure must not error the retrieval: %v", err)
	}
	if len(hits) != 1 || hits[0].RelPath != "kw.md" {
		t.Errorf("expected graceful BM25 fallback, got %+v", hits)
	}
}
