package index

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

type stubLLM struct {
	mu      sync.Mutex
	calls   int
	respond func(prompt string) string
}

func (s *stubLLM) Name() string { return "stub" }
func (s *stubLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	s.calls++
	respond := s.respond
	s.mu.Unlock()

	body := ""
	for _, m := range req.Messages {
		body += m.Content + "\n"
	}
	resp := `{"summary": "stub summary", "keywords": ["alpha", "beta"]}`
	if respond != nil {
		resp = respond(body)
	}
	return &llm.ChatResponse{Content: resp}, nil
}

func (s *stubLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
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

	ix := New([]string{notesDir}, store, lc)
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Added != 2 || res.Updated != 0 {
		t.Errorf("expected 2 added/0 updated, got %+v", res)
	}
	if lc.callCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", lc.callCount())
	}

	files, _ := store.List(context.Background())
	if len(files) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(files))
	}
}

func TestScanSkipsUnchangedFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A\nhello")

	ix := New([]string{notesDir}, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := lc.callCount()

	// Second scan: nothing changed → 0 LLM calls.
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lc.callCount() != first {
		t.Errorf("second scan should skip unchanged files, llm went from %d to %d", first, lc.callCount())
	}
}

func TestScanRemovesMissingFiles(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A")
	writeNote(t, notesDir, "b.md", "# B")

	ix := New([]string{notesDir}, store, lc)
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

	ix := New([]string{notesDir}, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := lc.callCount()

	// Tweak content and bump mtime forward.
	time.Sleep(1100 * time.Millisecond)
	writeNote(t, notesDir, "a.md", "second")

	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lc.callCount() != first+1 {
		t.Errorf("expected one more LLM call after modification, got %d→%d", first, lc.callCount())
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

	ix := New([]string{notesDir}, store, lc)
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("expected 1 added (a.md only), got %+v", res)
	}
}

func TestScanReusesSummaryOnMove(t *testing.T) {
	notesDir, store, lc := setupNotes(t)
	writeNote(t, notesDir, "original.md", "# Content\nhello world")

	ix := New([]string{notesDir}, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := lc.callCount()

	// Move: rename original.md → moved.md (same content, new path).
	if err := os.Rename(
		filepath.Join(notesDir, "original.md"),
		filepath.Join(notesDir, "moved.md"),
	); err != nil {
		t.Fatal(err)
	}

	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// No extra LLM call: summary was inherited from the old record.
	if lc.callCount() != callsAfterFirst {
		t.Errorf("expected no new LLM calls on move, got %d extra", lc.callCount()-callsAfterFirst)
	}
	if res.Moved != 1 {
		t.Errorf("expected 1 moved, got %+v", res)
	}
	if res.Removed != 1 {
		t.Errorf("expected 1 removed (old path), got %+v", res)
	}

	files, _ := store.List(context.Background())
	if len(files) != 1 || files[0].RelPath != "moved.md" {
		t.Errorf("expected only moved.md in index, got %+v", files)
	}
}

func TestScanRecoversFromUnparseableLLM(t *testing.T) {
	notesDir, store, _ := setupNotes(t)
	writeNote(t, notesDir, "a.md", "# A")

	lc := &stubLLM{respond: func(string) string { return "this is not JSON" }}
	ix := New([]string{notesDir}, store, lc)

	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Failed != 0 {
		t.Errorf("expected fallback summary to keep us going, got %+v", res)
	}
}

func TestScanMultipleDirs(t *testing.T) {
	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "work")
	dirB := filepath.Join(tmp, "personal")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}

	writeNote(t, dirA, "alpha.md", "# Alpha\nhello from work")
	writeNote(t, dirB, "beta.md", "# Beta\nhello from personal")

	store, err := storage.Open(filepath.Join(tmp, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	lc := &stubLLM{}
	ix := New([]string{dirA, dirB}, store, lc)
	res, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Added != 2 {
		t.Errorf("expected 2 added, got %+v", res)
	}

	files, _ := store.List(context.Background())
	if len(files) != 2 {
		t.Fatalf("expected 2 rows in DB, got %d", len(files))
	}

	// Both relPaths must carry the dir basename as prefix.
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.RelPath] = true
	}
	if !paths["work/alpha.md"] {
		t.Errorf("expected relPath 'work/alpha.md', got paths: %v", paths)
	}
	if !paths["personal/beta.md"] {
		t.Errorf("expected relPath 'personal/beta.md', got paths: %v", paths)
	}

	// Second scan with same content → 0 LLM calls, 2 skipped.
	callsBefore := lc.callCount()
	res2, err := ix.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lc.callCount() != callsBefore {
		t.Errorf("second scan should skip all files, got extra LLM calls")
	}
	if res2.Skipped != 2 {
		t.Errorf("expected 2 skipped on second scan, got %+v", res2)
	}
}

func TestScanMultipleDirsDeleteReconciliation(t *testing.T) {
	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "work")
	dirB := filepath.Join(tmp, "personal")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, dirA, "a.md", "A")
	writeNote(t, dirB, "b.md", "B")

	store, err := storage.Open(filepath.Join(tmp, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	lc := &stubLLM{}
	ix := New([]string{dirA, dirB}, store, lc)
	if _, err := ix.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Remove b.md and re-scan → personal/b.md must disappear from index.
	if err := os.Remove(filepath.Join(dirB, "b.md")); err != nil {
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
	if len(files) != 1 || files[0].RelPath != "work/a.md" {
		t.Errorf("expected only work/a.md, got %+v", files)
	}
}
