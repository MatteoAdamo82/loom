package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakePDFOnDisk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPDFSupportsRecognisesExtension(t *testing.T) {
	if !(PDF{}).Supports("/tmp/x.pdf") {
		t.Errorf("PDF.Supports(/tmp/x.pdf) should be true")
	}
	if (PDF{}).Supports("/tmp/x.txt") {
		t.Errorf("PDF must not claim non-pdf files")
	}
	if (PDF{}).Supports("https://example.com/x.pdf") {
		t.Errorf("URLs are HTML's territory, even when ending in .pdf")
	}
}

// On a non-real PDF, ledongthuc errors out. We expect ExtractContext to
// surface that error rather than silently returning empty content.
func TestPDFExtractRejectsBrokenFile(t *testing.T) {
	path := fakePDFOnDisk(t)
	_, err := PDF{}.Extract(path)
	if err == nil {
		t.Fatal("expected error on a non-PDF blob")
	}
	// Either a ledongthuc parse error or a "no extractable text" error are
	// acceptable — both indicate the extractor is not silently succeeding.
	if !strings.Contains(err.Error(), "ledongthuc") && !strings.Contains(err.Error(), "no extractable text") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// composeMarkdown drops empty pages and tags OCR pages.
func TestComposeMarkdown(t *testing.T) {
	out := composeMarkdown([]pdfPage{
		{number: 1, text: "hello"},
		{number: 2, text: ""},
		{number: 3, text: "world", viaOCR: true},
	})
	if !strings.Contains(out, "## Page 1") {
		t.Errorf("missing Page 1 header: %q", out)
	}
	if strings.Contains(out, "## Page 2") {
		t.Errorf("empty page should be skipped: %q", out)
	}
	if !strings.Contains(out, "## Page 3 *(OCR)*") {
		t.Errorf("OCR page should be tagged: %q", out)
	}
}
