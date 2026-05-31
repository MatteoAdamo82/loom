package extract

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// HTML.Supports
// ---------------------------------------------------------------------------

func TestHTMLSupportsLocalFiles(t *testing.T) {
	h := HTML{}
	for _, name := range []string{"page.html", "page.htm", "PAGE.HTML"} {
		if !h.Supports(name) {
			t.Errorf("expected Supports=true for %q", name)
		}
	}
}

func TestHTMLSupportsHTTPURLs(t *testing.T) {
	h := HTML{}
	for _, u := range []string{"http://example.com", "https://example.com/article"} {
		if !h.Supports(u) {
			t.Errorf("expected Supports=true for %q", u)
		}
	}
}

func TestHTMLDoesNotSupportOtherTypes(t *testing.T) {
	h := HTML{}
	for _, src := range []string{"notes.md", "doc.pdf", "data.csv"} {
		if h.Supports(src) {
			t.Errorf("expected Supports=false for %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// HTML.Extract — local file
// ---------------------------------------------------------------------------

func TestHTMLExtractLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "article.html")

	content := `<!DOCTYPE html>
<html>
<head><title>Test Article</title></head>
<body>
  <article>
    <h1>Hello World</h1>
    <p>This is the article body with enough words to pass readability heuristics and be considered real content.</p>
    <p>Another paragraph with more content to make the article long enough for extraction.</p>
  </article>
</body>
</html>`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h := HTML{}
	doc, err := h.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if doc.Kind != "html" {
		t.Errorf("kind = %q, want html", doc.Kind)
	}
	if doc.Hash == "" {
		t.Error("expected non-empty hash")
	}
	if !strings.HasPrefix(doc.URI, "file://") {
		t.Errorf("URI = %q, expected file:// prefix", doc.URI)
	}
}

func TestHTMLExtractMissingFile(t *testing.T) {
	h := HTML{}
	_, err := h.Extract("/nonexistent/path/file.html")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// HTML.Extract — HTTP URL (mock server)
// ---------------------------------------------------------------------------

func TestHTMLExtractURL(t *testing.T) {
	body := `<!DOCTYPE html>
<html>
<head><title>Remote Article</title></head>
<body>
  <article>
    <h1>Remote Content</h1>
    <p>Fetched from a remote server. This paragraph is long enough to be extracted by the readability algorithm and contains meaningful text.</p>
    <p>Second paragraph adds more context and substance to make the content extractable.</p>
  </article>
</body>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	h := HTML{}
	doc, err := h.Extract(srv.URL)
	if err != nil {
		t.Fatalf("Extract URL: %v", err)
	}
	if doc.Hash == "" {
		t.Error("expected non-empty hash for URL content")
	}
	if doc.URI != srv.URL {
		t.Errorf("URI = %q, want %q", doc.URI, srv.URL)
	}
}

func TestHTMLExtractURLServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := HTML{}
	_, err := h.Extract(srv.URL)
	if err == nil {
		t.Fatal("expected error for server 500")
	}
}

// ---------------------------------------------------------------------------
// HTML in the default registry
// ---------------------------------------------------------------------------

func TestRegistryResolvesHTML(t *testing.T) {
	r := DefaultRegistry()
	ext, err := r.Resolve("page.html")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := ext.(HTML); !ok {
		t.Errorf("expected HTML extractor, got %T", ext)
	}
}

func TestRegistryResolvesHTTPURL(t *testing.T) {
	r := DefaultRegistry()
	ext, err := r.Resolve("http://example.com/article")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := ext.(HTML); !ok {
		t.Errorf("expected HTML extractor for URL, got %T", ext)
	}
}
