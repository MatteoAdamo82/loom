package extract

import (
	"net/http"
	"net/http/httptest"
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
	for _, p := range []string{"/tmp/x.html", "/tmp/x.htm", "https://example.com/", "http://blog.example.org/post"} {
		if !h.Supports(p) {
			t.Errorf("HTML should support %q", p)
		}
	}
	if h.Supports("/tmp/x.md") {
		t.Errorf("HTML should not claim md")
	}
}

func TestPDFSupports(t *testing.T) {
	if !(PDF{}.Supports("/tmp/x.pdf")) {
		t.Errorf("PDF should support .pdf path")
	}
	if (PDF{}.Supports("https://example.com/x.pdf")) {
		t.Errorf("PDF should ignore URLs (HTML extractor handles those)")
	}
}

func TestRegistryRoutesURLsToHTML(t *testing.T) {
	r := DefaultRegistry()
	e, err := r.Resolve("https://example.com/article")
	if err != nil {
		t.Fatalf("URL should resolve: %v", err)
	}
	if _, ok := e.(HTML); !ok {
		t.Errorf("URL extractor should be HTML, got %T", e)
	}
}

func TestHTMLFetchesURL(t *testing.T) {
	page := `<!doctype html>
<html><head><title>Live Page</title></head>
<body>
  <article>
    <h1>Live Page</h1>
    <p>Body served over HTTP for the integration test, with a unique
    canary phrase 'foxglove sentinel' to verify content arrives intact.</p>
  </article>
</body></html>`

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	doc, err := HTML{}.Extract(srv.URL + "/article")
	if err != nil {
		t.Fatalf("extract URL: %v", err)
	}
	if doc.Kind != "html" {
		t.Errorf("kind = %q", doc.Kind)
	}
	if doc.URI != srv.URL+"/article" {
		t.Errorf("URI = %q, want %q", doc.URI, srv.URL+"/article")
	}
	if doc.Title != "Live Page" {
		t.Errorf("title = %q", doc.Title)
	}
	if !strings.Contains(doc.Content, "foxglove sentinel") {
		t.Errorf("body content missing canary: %q", doc.Content)
	}
	if !strings.HasPrefix(gotUA, "loom/") {
		t.Errorf("User-Agent = %q, want loom/...", gotUA)
	}
	if len(doc.Hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(doc.Hash))
	}
}

func TestHTMLFollowsRedirectsAndUsesFinalURL(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Resolved</title></head>
<body><article><p>` + strings.Repeat("Resolved body sentence. ", 30) + `</p></article></body></html>`))
	}))
	defer final.Close()

	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/article", http.StatusFound)
	}))
	defer short.Close()

	doc, err := HTML{}.Extract(short.URL + "/s/abc")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.URI != final.URL+"/article" {
		t.Errorf("URI should be the final URL, got %q", doc.URI)
	}
}

func TestHTMLPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := (HTML{}).Extract(srv.URL + "/missing"); err == nil {
		t.Fatal("expected 404 to propagate as error")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status code: %v", err)
	}
}
