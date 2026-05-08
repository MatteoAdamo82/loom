package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func openTempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndList(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	err := s.Upsert(ctx, &File{
		RelPath:  "a.md",
		Hash:     "h1",
		MTime:    1000,
		Kind:     "md",
		Title:    "Note A",
		Content:  "hello world",
		Summary:  "this is a note about hello",
		Keywords: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	files, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 || files[0].RelPath != "a.md" {
		t.Fatalf("unexpected list: %+v", files)
	}
	if got := files[0].Keywords; len(got) != 2 || got[0] != "hello" {
		t.Errorf("keywords mismatch: %v", got)
	}
}

func TestUpsertReplaces(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	for _, body := range []string{"first", "second"} {
		if err := s.Upsert(ctx, &File{
			RelPath: "a.md", Hash: body, MTime: 1, Kind: "md",
			Title: "T", Content: body, Summary: body, Keywords: []string{body},
		}); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := s.List(ctx)
	if len(files) != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", len(files))
	}
	if files[0].Content != "second" {
		t.Errorf("upsert didn't replace: %q", files[0].Content)
	}
}

func TestSearchFTS(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	seed := []File{
		{RelPath: "carbonara.md", Hash: "1", MTime: 1, Kind: "md", Title: "Carbonara",
			Content: "guanciale pecorino uova", Summary: "ricetta della carbonara",
			Keywords: []string{"pasta", "carbonara"}},
		{RelPath: "amatriciana.md", Hash: "2", MTime: 1, Kind: "md", Title: "Amatriciana",
			Content: "guanciale pomodoro pecorino", Summary: "sugo amatriciana",
			Keywords: []string{"pasta", "amatriciana"}},
		{RelPath: "vacanze.md", Hash: "3", MTime: 1, Kind: "md", Title: "Vacanze",
			Content: "appunti viaggio sardegna", Summary: "diario di viaggio",
			Keywords: []string{"viaggio"}},
	}
	for _, f := range seed {
		if err := s.Upsert(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := s.Search(ctx, "carbonara", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].RelPath != "carbonara.md" {
		t.Errorf("expected carbonara.md as top hit, got %+v", hits)
	}

	hits, err = s.Search(ctx, "guanciale", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 hits for guanciale, got %d", len(hits))
	}
}

func TestDeleteRemovesFromFTS(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	_ = s.Upsert(ctx, &File{RelPath: "a.md", Hash: "1", MTime: 1, Kind: "md",
		Title: "Alpha", Content: "alpha beta", Summary: "alpha", Keywords: []string{"alpha"}})

	if err := s.Delete(ctx, "a.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, _ := s.Search(ctx, "alpha", 5)
	if len(hits) != 0 {
		t.Errorf("expected no FTS hits after delete, got %d", len(hits))
	}
}

func TestSearchSanitisesSyntax(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, &File{RelPath: "a.md", Hash: "1", MTime: 1, Kind: "md",
		Title: "Test", Content: "the cat sat on the mat", Summary: "feline antics",
		Keywords: []string{"cat", "mat"}})

	// User input with FTS syntax characters must not throw.
	hits, err := s.Search(ctx, `cat: "where" (sat)?`, 5)
	if err != nil {
		t.Fatalf("search must tolerate punctuation: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("expected at least one hit despite punctuation")
	}
}
