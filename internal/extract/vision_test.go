package extract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makePNG writes a tiny but valid 1×1 white PNG and returns its bytes.
func makePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	f, err := os.CreateTemp(t.TempDir(), "*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// makeOllamaServer starts a fake Ollama server that records the decoded request
// and replies with the given content string.
func makeOllamaServer(t *testing.T, replyContent string) (*httptest.Server, *ollamaVisionRequest) {
	t.Helper()
	var captured ollamaVisionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := ollamaVisionResponse{}
		resp.Message.Content = replyContent
		resp.Done = true
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// ---------------------------------------------------------------------------
// VisionExtractor.Supports
// ---------------------------------------------------------------------------

func TestVisionExtractorSupports(t *testing.T) {
	v := NewVisionExtractor("http://localhost:11434", "glm-ocr")
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".tiff", ".tif"} {
		if !v.Supports("file" + ext) {
			t.Errorf("expected Supports to return true for %q", ext)
		}
	}
}

func TestVisionExtractorDoesNotSupportOtherTypes(t *testing.T) {
	v := NewVisionExtractor("http://localhost:11434", "glm-ocr")
	for _, src := range []string{"doc.pdf", "notes.md", "page.html", "http://example.com"} {
		if v.Supports(src) {
			t.Errorf("expected Supports to return false for %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// VisionExtractor.OCR (via test seam)
// ---------------------------------------------------------------------------

func TestVisionExtractorOCRCallsOllamaAndReturnsContent(t *testing.T) {
	imgBytes := makePNG(t)
	srv, captured := makeOllamaServer(t, "# Invoice\nTotal: €42")

	v := &VisionExtractor{Endpoint: srv.URL, Model: "glm-ocr"}
	got, err := v.OCR(context.Background(), imgBytes)
	if err != nil {
		t.Fatalf("OCR: %v", err)
	}
	if got != "# Invoice\nTotal: €42" {
		t.Errorf("content = %q", got)
	}
	// Verify the image was base64-encoded and sent correctly.
	if len(captured.Messages) != 1 || len(captured.Messages[0].Images) != 1 {
		t.Fatalf("expected 1 message with 1 image, got: %+v", captured)
	}
	decoded, err := base64.StdEncoding.DecodeString(captured.Messages[0].Images[0])
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(imgBytes) {
		t.Error("image bytes were not transmitted correctly")
	}
	if captured.Model != "glm-ocr" {
		t.Errorf("model = %q, want glm-ocr", captured.Model)
	}
	if captured.Stream {
		t.Error("stream should be false")
	}
	// Regression: the request must cap num_ctx so Ollama doesn't allocate the
	// model's full default context (glm-ocr: 131072), which blows the KV cache
	// up to ~10 GB and swaps memory-constrained machines to a standstill.
	if captured.Options.NumCtx != ocrNumCtx {
		t.Errorf("num_ctx = %d, want %d", captured.Options.NumCtx, ocrNumCtx)
	}
	if captured.Options.NumPredict != ocrNumPredict {
		t.Errorf("num_predict = %d, want %d", captured.Options.NumPredict, ocrNumPredict)
	}
}

func TestVisionExtractorOCRErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	v := &VisionExtractor{Endpoint: srv.URL, Model: "nonexistent"}
	_, err := v.OCR(context.Background(), []byte("fake"))
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestVisionExtractorOCRUsesTestSeam(t *testing.T) {
	called := false
	v := &VisionExtractor{
		ocrFn: func(_ context.Context, _ []byte) (string, error) {
			called = true
			return "seam result", nil
		},
	}
	got, err := v.OCR(context.Background(), []byte("img"))
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("test seam was not called")
	}
	if got != "seam result" {
		t.Errorf("content = %q", got)
	}
}

// ---------------------------------------------------------------------------
// VisionExtractor.Extract
// ---------------------------------------------------------------------------

func TestVisionExtractorExtractReturnsDocument(t *testing.T) {
	imgBytes := makePNG(t)

	// Write the PNG to a temp file so Extract() can read it.
	dir := t.TempDir()
	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, _ := makeOllamaServer(t, "# Receipt\nCoffee €2.50")

	v := &VisionExtractor{Endpoint: srv.URL, Model: "glm-ocr"}
	doc, err := v.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(doc.Content, "Coffee") {
		t.Errorf("content = %q", doc.Content)
	}
	if doc.Kind != "png" {
		t.Errorf("kind = %q, want png", doc.Kind)
	}
	if doc.Hash == "" {
		t.Error("expected non-empty hash")
	}
	// URI should be a file:// URL.
	if !strings.HasPrefix(doc.URI, "file://") {
		t.Errorf("URI = %q", doc.URI)
	}
}

func TestVisionExtractorExtractPicksTitleFromMarkdownHeading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, makePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := makeOllamaServer(t, "# My Document Title\nBody text here.")

	v := &VisionExtractor{Endpoint: srv.URL, Model: "glm-ocr"}
	doc, err := v.Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "My Document Title" {
		t.Errorf("title = %q, want 'My Document Title'", doc.Title)
	}
}

func TestVisionExtractorExtractFallsBackToFilenameTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invoice-2024.png")
	if err := os.WriteFile(path, makePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := makeOllamaServer(t, "Plain text, no heading.")

	v := &VisionExtractor{Endpoint: srv.URL, Model: "glm-ocr"}
	doc, _ := v.Extract(path)
	if doc.Title != "invoice-2024" {
		t.Errorf("title = %q, want 'invoice-2024'", doc.Title)
	}
}

func TestVisionExtractorExtractErrorOnEmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.png")
	if err := os.WriteFile(path, makePNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := makeOllamaServer(t, "   ") // whitespace-only response

	v := &VisionExtractor{Endpoint: srv.URL, Model: "glm-ocr"}
	_, err := v.Extract(path)
	if err == nil {
		t.Fatal("expected error for empty OCR content")
	}
}

// ---------------------------------------------------------------------------
// NewRegistryWithOCR integration
// ---------------------------------------------------------------------------

func TestNewRegistryWithOCRSupportsPNGAndJPG(t *testing.T) {
	r := NewRegistryWithOCR("http://localhost:11434", "glm-ocr")
	for _, f := range []string{"photo.png", "scan.jpg", "diagram.jpeg", "picture.webp"} {
		if !r.Supports(f) {
			t.Errorf("registry should support %q when OCR is configured", f)
		}
	}
}

func TestDefaultRegistryDoesNotSupportImages(t *testing.T) {
	r := DefaultRegistry()
	for _, f := range []string{"photo.png", "scan.jpg"} {
		if r.Supports(f) {
			t.Errorf("default registry should NOT support %q (no OCR configured)", f)
		}
	}
}

func TestNewRegistryWithOCRStillSupportsCoreTypes(t *testing.T) {
	r := NewRegistryWithOCR("http://localhost:11434", "glm-ocr")
	for _, f := range []string{"notes.md", "doc.pdf", "page.html", "readme.txt"} {
		if !r.Supports(f) {
			t.Errorf("registry should still support %q", f)
		}
	}
}
