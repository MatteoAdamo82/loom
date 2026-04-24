package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextExtractor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "article.md")
	body := "# My Heading\n\nBody text with accènted.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := Text{}.Extract(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Kind != "md" {
		t.Errorf("kind = %q", doc.Kind)
	}
	if doc.Title != "My Heading" {
		t.Errorf("title = %q", doc.Title)
	}
	if !strings.HasPrefix(doc.URI, "file://") {
		t.Errorf("uri = %q", doc.URI)
	}
	if len(doc.Hash) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(doc.Hash))
	}
	if doc.Content != body {
		t.Errorf("content mismatch")
	}
}

func TestTextExtractorFallbackTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-heading.txt")
	if err := os.WriteFile(path, []byte("just a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Text{}.Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "no-heading" {
		t.Errorf("title = %q", doc.Title)
	}
	if doc.Kind != "txt" {
		t.Errorf("kind = %q", doc.Kind)
	}
}

func TestRegistryResolve(t *testing.T) {
	r := DefaultRegistry()
	if _, err := r.Resolve("/tmp/x.md"); err != nil {
		t.Errorf("md should resolve: %v", err)
	}
	if _, err := r.Resolve("/tmp/x.pdf"); err == nil {
		t.Error("pdf should not resolve in MVP registry")
	}
}
