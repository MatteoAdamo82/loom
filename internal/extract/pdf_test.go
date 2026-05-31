package extract

import (
	"context"
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

// ---------------------------------------------------------------------------
// Vision OCR path
// ---------------------------------------------------------------------------

// TestPDFShouldTryOCRWithVisionOnlyNeedsPdftoppm verifies that when
// ocrVision is set, shouldTryOCR does not require tesseract (only pdftoppm).
// We test via the function that drives the decision, not by shelling out.
func TestPDFVisionOCRIsUsedWhenSet(t *testing.T) {
	visionCalled := false
	p := PDF{
		ocrVision: func(_ context.Context, _ []byte) (string, error) {
			visionCalled = true
			return "vision text", nil
		},
		// renderPages seam: returns one fake "image" path; we write a dummy
		// file so that os.ReadFile in runOCROnPages succeeds.
		renderPages: func(_ context.Context, _ string, outDir string, _ int) ([]string, error) {
			imgPath := filepath.Join(outDir, "page-1.png")
			if err := os.WriteFile(imgPath, []byte("fakepng"), 0o644); err != nil {
				return nil, err
			}
			return []string{imgPath}, nil
		},
	}

	pages := []pdfPage{{number: 1, text: ""}} // empty page triggers OCR
	result, err := p.runOCROnPages(context.Background(), "doc.pdf", pages)
	if err != nil {
		t.Fatalf("runOCROnPages: %v", err)
	}
	if !visionCalled {
		t.Error("vision OCR function was not called")
	}
	if len(result) == 0 || result[0].text != "vision text" {
		t.Errorf("unexpected page text: %+v", result)
	}
	if !result[0].viaOCR {
		t.Error("viaOCR flag should be set for vision-processed page")
	}
}

func TestPDFTesseractPathUsedWhenVisionNotSet(t *testing.T) {
	tesseractCalled := false
	p := PDF{
		ocrImage: func(_ context.Context, _ string, _ string) (string, error) {
			tesseractCalled = true
			return "tesseract text", nil
		},
		renderPages: func(_ context.Context, _ string, outDir string, _ int) ([]string, error) {
			imgPath := filepath.Join(outDir, "page-1.png")
			if err := os.WriteFile(imgPath, []byte("fakepng"), 0o644); err != nil {
				return nil, err
			}
			return []string{imgPath}, nil
		},
	}

	pages := []pdfPage{{number: 1, text: ""}}
	result, err := p.runOCROnPages(context.Background(), "doc.pdf", pages)
	if err != nil {
		t.Fatalf("runOCROnPages: %v", err)
	}
	if !tesseractCalled {
		t.Error("tesseract function was not called when vision is not set")
	}
	if len(result) == 0 || result[0].text != "tesseract text" {
		t.Errorf("unexpected page text: %+v", result)
	}
}

func TestPDFVisionSkipsPagesThatAlreadyHaveText(t *testing.T) {
	visionCalls := 0
	p := PDF{
		ocrVision: func(_ context.Context, _ []byte) (string, error) {
			visionCalls++
			return "vision text", nil
		},
		renderPages: func(_ context.Context, _ string, outDir string, _ int) ([]string, error) {
			paths := make([]string, 2)
			for i := range paths {
				paths[i] = filepath.Join(outDir, "page-"+string(rune('1'+i))+".png")
				_ = os.WriteFile(paths[i], []byte("fakepng"), 0o644)
			}
			return paths, nil
		},
	}

	pages := []pdfPage{
		{number: 1, text: "already has text here"}, // should be skipped
		{number: 2, text: ""},                       // should be OCR'd
	}
	result, err := p.runOCROnPages(context.Background(), "doc.pdf", pages)
	if err != nil {
		t.Fatalf("runOCROnPages: %v", err)
	}
	if visionCalls != 1 {
		t.Errorf("expected 1 vision call (only empty page), got %d", visionCalls)
	}
	if result[0].text != "already has text here" {
		t.Errorf("page 1 text should not be overwritten: %q", result[0].text)
	}
	if result[1].text != "vision text" {
		t.Errorf("page 2 should have vision text: %q", result[1].text)
	}
}

// ---------------------------------------------------------------------------
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
