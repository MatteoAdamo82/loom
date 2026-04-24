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
	for _, path := range []string{"/tmp/x.md", "/tmp/x.txt", "/tmp/x.pdf", "/tmp/x.html", "/tmp/x.htm"} {
		if _, err := r.Resolve(path); err != nil {
			t.Errorf("%s should resolve: %v", path, err)
		}
	}
	if _, err := r.Resolve("/tmp/x.docx"); err == nil {
		t.Error("docx should not resolve yet")
	}
}

func TestHTMLExtractor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	body := `<!doctype html>
<html><head><title>My Article Title</title></head>
<body>
  <nav>menu links to skip</nav>
  <article>
    <h1>My Article Title</h1>
    <p>This is the body of the article. It contains real prose
    that the readability extractor should keep.</p>
    <p>A second paragraph with more information about the topic.</p>
  </article>
  <footer>copyright 2026</footer>
</body></html>`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := HTML{}.Extract(path)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Kind != "html" {
		t.Errorf("kind = %q", doc.Kind)
	}
	if doc.Title != "My Article Title" {
		t.Errorf("title = %q", doc.Title)
	}
	if !strings.Contains(doc.Content, "second paragraph") {
		t.Errorf("article body missing: %q", doc.Content)
	}
	if strings.Contains(doc.Content, "copyright 2026") {
		t.Errorf("readability did not strip footer chrome: %q", doc.Content)
	}
	if len(doc.Hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(doc.Hash))
	}
}

func TestHTMLSupports(t *testing.T) {
	h := HTML{}
	for _, ext := range []string{"html", "htm"} {
		if !h.Supports(ext) {
			t.Errorf("HTML should support .%s", ext)
		}
	}
	if h.Supports("md") {
		t.Errorf("HTML should not claim md")
	}
}

func TestPDFSupports(t *testing.T) {
	if !(PDF{}.Supports("pdf")) {
		t.Errorf("PDF.Supports(pdf) should be true")
	}
}
