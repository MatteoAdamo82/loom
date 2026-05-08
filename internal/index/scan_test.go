package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type stubLLM struct {
	calls   int
	respond func(prompt string) string
}

func (s *stubLLM) Name() string { return "stub" }
func (s *stubLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.calls++
	body := ""
	for _, m := range req.Messages {
		body += m.Content + "\n"
	}
	resp := `{"summary": "stub summary", "keywords": ["alpha", "beta"]}`
	if s.respond != nil {
		resp = s.respond(body)
	}
	return &llm.ChatResponse{Content: resp}, nil
}

func setupNotes(t *testing.T) (notesDir string, store *storage.Store, lc *stubLLM) {
	t.Helper()
	tmp := t.TempDir()
	notesDir = filepath.Join(tmp, "loom")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(tmp, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	lc = &stubLLM{}
	return
}

func writeNote(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanIndexesNewFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A\nhello world")
	writeNote(t, notesDir, "b.md", "# B\nciao mondo")

	ix := New(notesDir, store, lc)
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Added != 2 || res.Updated != 0 {
		t.Errorf("expected 2 added/0 updated, got %+v", res)
	}
	if lc.calls != 2 {
		t.Errorf("expected 2 LLM calls, got %d", lc.calls)
	}

	files, _ := store.List(context.Background())
	if len(files) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(files))
	}
}

func TestScanSkipsUnchangedFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A\nhello")

	ix := New(notesDir, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := lc.calls

	// Second scan: nothing changed → 0 LLM calls.
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lc.calls != first {
		t.Errorf("second scan should skip unchanged files, llm went from %d to %d", first, lc.calls)
	}
}

func TestScanRemovesMissingFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A")
	writeNote(t, notesDir, "b.md", "# B")

	ix := New(notesDir, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Delete b.md from disk and re-scan.
	if err := os.Remove(filepath.Join(notesDir, "b.md")); err != nil {
		t.Fatal(err)
	}
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Errorf("expected 1 removed, got %+v", res)
	}

	files, _ := store.List(context.Background())
	if len(files) != 1 || files[0].RelPath != "a.md" {
		t.Errorf("expected only a.md to remain, got %+v", files)
	}
}

func TestScanReProcessesModifiedFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "first")

	ix := New(notesDir, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := lc.calls

	// Tweak content and bump mtime forward.
	time.Sleep(1100 * time.Millisecond)
	writeNote(t, notesDir, "a.md", "second")

	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lc.calls != first+1 {
		t.Errorf("expected one more LLM call after modification, got %d→%d", first, lc.calls)
	}
}

func TestScanSkipsHiddenAndUnsupported(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# good")
	writeNote(t, notesDir, ".hidden.md", "ignored")
	writeNote(t, notesDir, "image.png", "not text")
	if err := os.MkdirAll(filepath.Join(notesDir, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeNote(t, filepath.Join(notesDir, ".obsidian"), "config.md", "should not be scanned")

	ix := New(notesDir, store, lc)
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("expected 1 added (a.md only), got %+v", res)
	}
}

func TestScanRecoversFromUnparseableLLM(t *testing.T) {
	notesDir, store, _ := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A")

	lc := &stubLLM{respond: func(string) string { return "this is not JSON" }}
	ix := New(notesDir, store, lc)

	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Failed != 0 {
		t.Errorf("expected fallback summary to keep us going, got %+v", res)
	}
}
