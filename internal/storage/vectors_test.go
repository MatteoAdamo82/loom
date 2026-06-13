package storage

import (
	"context"
	"errors"
	"testing"
)

// seedFile is a tiny helper for the vector tests.
func seedFile(t *testing.T, s *Store, relPath, content string) {
	t.Helper()
	if err := s.Upsert(context.Background(), &File{
		RelPath: relPath, Hash: relPath, MTime: 1, Kind: "md",
		Title: relPath, Content: content, Summary: content, Keywords: []string{relPath},
	}); err != nil {
		t.Fatalf("seed %s: %v", relPath, err)
	}
}

func TestVectorRoundTrip(t *testing.T) {
	in := []float32{1.5, -2.25, 0, 3.125}
	out := decodeVector(encodeVector(in))
	if len(out) != len(in) {
		t.Fatalf("len mismatch: %d vs %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("element %d: %v != %v", i, out[i], in[i])
		}
	}
}

func TestVectorSearchRanksByCosine(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	seedFile(t, s, "a.md", "alpha")
	seedFile(t, s, "b.md", "beta")
	seedFile(t, s, "c.md", "gamma")

	// a is identical to the query direction, c is close, b is orthogonal.
	mustSetVector(t, s, "a.md", []float32{1, 0, 0})
	mustSetVector(t, s, "c.md", []float32{0.9, 0.1, 0})
	mustSetVector(t, s, "b.md", []float32{0, 1, 0})

	hits, err := s.VectorSearch(ctx, []float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(hits))
	}
	if hits[0].RelPath != "a.md" || hits[1].RelPath != "c.md" || hits[2].RelPath != "b.md" {
		t.Errorf("wrong cosine order: %s, %s, %s", hits[0].RelPath, hits[1].RelPath, hits[2].RelPath)
	}
	if hits[0].Rank < hits[1].Rank || hits[1].Rank < hits[2].Rank {
		t.Errorf("ranks not descending: %v", []float64{hits[0].Rank, hits[1].Rank, hits[2].Rank})
	}
}

func TestVectorSearchSkipsDimensionMismatch(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	seedFile(t, s, "a.md", "alpha")
	seedFile(t, s, "b.md", "beta")
	mustSetVector(t, s, "a.md", []float32{1, 0}) // 2-dim — stale model
	mustSetVector(t, s, "b.md", []float32{1, 0, 0})

	hits, err := s.VectorSearch(ctx, []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 1 || hits[0].RelPath != "b.md" {
		t.Errorf("expected only the 3-dim vector to match, got %+v", hits)
	}
}

func TestSetVectorUnknownPathIsNoop(t *testing.T) {
	s := openTempStore(t)
	if err := s.SetVector(context.Background(), "does-not-exist.md", []float32{1, 2, 3}, "m"); err != nil {
		t.Fatalf("unknown path should be a silent no-op, got: %v", err)
	}
	ok, _ := s.HasVectors(context.Background())
	if ok {
		t.Error("expected no vectors stored for unknown path")
	}
}

func TestVectorCascadesOnFileDelete(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	seedFile(t, s, "a.md", "alpha")
	mustSetVector(t, s, "a.md", []float32{1, 0, 0})

	if err := s.Delete(ctx, "a.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := s.HasVectors(ctx); ok {
		t.Error("deleting the file should cascade-delete its vector")
	}
}

func TestHybridSearchFusesBothSignals(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	seedFile(t, s, "kw.md", "guanciale pecorino uova")   // matches BM25 query
	seedFile(t, s, "vec.md", "completely unrelated text") // only the vector matches
	seedFile(t, s, "noise.md", "nothing relevant here")

	mustSetVector(t, s, "kw.md", []float32{0, 1, 0})
	mustSetVector(t, s, "vec.md", []float32{1, 0, 0})
	mustSetVector(t, s, "noise.md", []float32{0, 0, 1})

	// Query: BM25 term "guanciale" favours kw.md; query vector favours vec.md.
	hits, err := s.HybridSearch(ctx, "guanciale", []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.RelPath] = true
	}
	if !seen["kw.md"] {
		t.Error("hybrid should surface the BM25 match kw.md")
	}
	if !seen["vec.md"] {
		t.Error("hybrid should surface the vector match vec.md")
	}
}

func TestHybridSearchEmptyIndex(t *testing.T) {
	s := openTempStore(t)
	_, err := s.HybridSearch(context.Background(), "anything", []float32{1, 0, 0}, 5)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on empty index, got %v", err)
	}
}

func mustSetVector(t *testing.T, s *Store, relPath string, vec []float32) {
	t.Helper()
	if err := s.SetVector(context.Background(), relPath, vec, "test-model"); err != nil {
		t.Fatalf("set vector %s: %v", relPath, err)
	}
}
