package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)

// OCRMode controls when the OCR fallback runs.
type OCRMode string

const (
	OCROff    OCRMode = "off"
	OCRAuto   OCRMode = "auto"   // OCR pages without selectable text
	OCRAlways OCRMode = "always" // OCR every page even if ledongthuc found text
)

// minTextPerPage is the heuristic threshold below which a page is considered
// "image-only" and gets rerouted to OCR. 16 chars is enough to dodge spurious
// PDF metadata strings that sometimes sneak through.
const minTextPerPage = 16

// PDF turns a .pdf file into a Document. It runs ledongthuc/pdf first (pure
// Go, fast, no deps) and falls back to a pdftoppm + tesseract pipeline on
// pages that come out empty. The composed Markdown is cached at CacheDir
// keyed by the file's sha256 so re-ingest is instantaneous.
type PDF struct {
	OCRMode      OCRMode
	OCRLanguages string
	CacheDir     string
	OCRDPI       int

	// renderPages and ocrImage are test seams. Production callers should
	// leave them nil; the package supplies real implementations that shell
	// out to pdftoppm and tesseract.
	renderPages func(ctx context.Context, pdfPath, outDir string, dpi int) ([]string, error)
	ocrImage    func(ctx context.Context, imagePath, languages string) (string, error)
}

func (PDF) Supports(source string) bool {
	return !IsURL(source) && extOf(source) == "pdf"
}

func (p PDF) Extract(source string) (*Document, error) {
	return p.ExtractContext(context.Background(), source)
}

// ExtractContext is the workhorse: hash → cache check → ledongthuc per page →
// OCR fallback per empty page → compose markdown → write cache.
func (p PDF) ExtractContext(ctx context.Context, path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	hash := sha256.Sum256(raw)
	hashHex := hex.EncodeToString(hash[:])

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	uri := (&url.URL{Scheme: "file", Path: abs}).String()

	// Cache check.
	if cached, ok := p.readCache(hashHex); ok {
		title := pdfTitle(path, cached)
		return &Document{
			URI:     uri,
			Kind:    "pdf",
			Title:   title,
			Content: cached,
			Hash:    hashHex,
		}, nil
	}

	// Stage 1: ledongthuc. We collect text per page so OCR can target the
	// empty ones.
	pages, err := readPagesWithLedongthuc(path)
	if err != nil {
		return nil, fmt.Errorf("ledongthuc: %w", err)
	}

	// Stage 2: OCR pages that need it.
	mode := p.OCRMode
	if mode == "" {
		mode = OCRAuto
	}
	if mode != OCROff && p.needsOCR(pages, mode) {
		ocred, ocrErr := p.runOCROnPages(ctx, path, pages, mode)
		if ocrErr == nil {
			pages = ocred
		} else {
			// We tolerate OCR failure: ledongthuc text (if any) survives,
			// the caller still gets *something*. The error is plumbed up
			// only if the final composition would be empty.
			if !pagesHaveText(pages) {
				return nil, fmt.Errorf("ocr fallback: %w", ocrErr)
			}
		}
	}

	content := composeMarkdown(pages)
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("pdf: no extractable text (consider OCRing the file first, or install poppler+tesseract for built-in OCR)")
	}

	if err := p.writeCache(hashHex, content); err != nil {
		// Cache failure is non-fatal — log via stderr so the user sees it
		// but still gets the extraction result.
		fmt.Fprintf(os.Stderr, "loom: pdf cache write failed: %v\n", err)
	}

	title := pdfTitle(path, content)
	return &Document{
		URI:     uri,
		Kind:    "pdf",
		Title:   title,
		Content: content,
		Hash:    hashHex,
	}, nil
}

// needsOCR decides whether stage 2 should run for the current OCR mode.
func (p PDF) needsOCR(pages []pdfPage, mode OCRMode) bool {
	if mode == OCRAlways {
		return true
	}
	for _, pg := range pages {
		if len(strings.TrimSpace(pg.text)) < minTextPerPage {
			return true
		}
	}
	return false
}

func pagesHaveText(pages []pdfPage) bool {
	for _, p := range pages {
		if strings.TrimSpace(p.text) != "" {
			return true
		}
	}
	return false
}

// runOCROnPages renders the PDF to PNGs and OCRs each "empty" page (or every
// page in OCRAlways mode). Pages that already have text from ledongthuc are
// preserved untouched in OCRAuto mode.
func (p PDF) runOCROnPages(ctx context.Context, pdfPath string, pages []pdfPage, mode OCRMode) ([]pdfPage, error) {
	render := p.renderPages
	if render == nil {
		render = renderPagesPdftoppm
	}
	ocr := p.ocrImage
	if ocr == nil {
		ocr = ocrImageTesseract
	}

	tmp, err := os.MkdirTemp("", "loom-pdf-ocr-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	dpi := p.OCRDPI
	if dpi <= 0 {
		dpi = 300
	}
	images, err := render(ctx, pdfPath, tmp, dpi)
	if err != nil {
		return nil, fmt.Errorf("render pages: %w", err)
	}

	// Make sure pages slice is at least as long as the rendered set so we
	// can attach OCR text to pages that ledongthuc missed entirely.
	for len(pages) < len(images) {
		pages = append(pages, pdfPage{number: len(pages) + 1})
	}

	langs := p.OCRLanguages
	if langs == "" {
		langs = "eng"
	}

	for i, img := range images {
		pg := &pages[i]
		// In OCRAuto, skip pages that already have substantive text.
		if mode == OCRAuto && len(strings.TrimSpace(pg.text)) >= minTextPerPage {
			continue
		}
		text, err := ocr(ctx, img, langs)
		if err != nil {
			// One bad page shouldn't kill the run.
			fmt.Fprintf(os.Stderr, "loom: tesseract failed on page %d: %v\n", i+1, err)
			continue
		}
		pg.text = strings.TrimSpace(text)
		pg.viaOCR = true
	}
	return pages, nil
}

// composeMarkdown joins page texts under "## Page N" headers. Empty pages are
// skipped.
func composeMarkdown(pages []pdfPage) string {
	var b strings.Builder
	for _, pg := range pages {
		text := strings.TrimSpace(pg.text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "## Page %d", pg.number)
		if pg.viaOCR {
			b.WriteString(" *(OCR)*")
		}
		b.WriteString("\n\n")
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// readCache returns the cached Markdown body if it exists, plus whether a hit
// was found.
func (p PDF) readCache(hashHex string) (string, bool) {
	if p.CacheDir == "" {
		return "", false
	}
	path := filepath.Join(p.CacheDir, hashHex+".md")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (p PDF) writeCache(hashHex, content string) error {
	if p.CacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(p.CacheDir, 0o755); err != nil {
		return fmt.Errorf("ensure cache dir: %w", err)
	}
	path := filepath.Join(p.CacheDir, hashHex+".md")
	return os.WriteFile(path, []byte(content), 0o644)
}

// ---------------------------------------------------------------------------
// stage 1: ledongthuc per-page extraction
// ---------------------------------------------------------------------------

type pdfPage struct {
	number int
	text   string
	viaOCR bool
}

func readPagesWithLedongthuc(path string) ([]pdfPage, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	total := r.NumPage()
	pages := make([]pdfPage, 0, total)
	for i := 1; i <= total; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			pages = append(pages, pdfPage{number: i})
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			// Tolerate per-page parse errors — those pages just go to OCR.
			pages = append(pages, pdfPage{number: i})
			continue
		}
		pages = append(pages, pdfPage{number: i, text: strings.TrimSpace(text)})
	}
	return pages, nil
}

// ---------------------------------------------------------------------------
// stage 2 backends: real subprocess calls (overridable via PDF struct fields)
// ---------------------------------------------------------------------------

// renderPagesPdftoppm shells out to `pdftoppm -r <dpi> -png pdfPath outDir/page`
// and returns the sorted list of generated PNG paths.
func renderPagesPdftoppm(ctx context.Context, pdfPath, outDir string, dpi int) ([]string, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, fmt.Errorf("pdftoppm not in PATH (install Poppler: brew install poppler / apt install poppler-utils): %w", err)
	}
	cmd := exec.CommandContext(ctx, "pdftoppm",
		"-r", fmt.Sprintf("%d", dpi),
		"-png",
		pdfPath,
		filepath.Join(outDir, "page"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pdftoppm: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// pdftoppm names files page-1.png, page-2.png, ...; collect and sort.
	var images []string
	err = filepath.WalkDir(outDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(p), ".png") {
			images = append(images, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(images)
	return images, nil
}

// ocrImageTesseract runs `tesseract <imagePath> stdout -l <languages>` and
// returns the recognized text.
func ocrImageTesseract(ctx context.Context, imagePath, languages string) (string, error) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", fmt.Errorf("tesseract not in PATH (install: brew install tesseract / apt install tesseract-ocr): %w", err)
	}
	cmd := exec.CommandContext(ctx, "tesseract", imagePath, "stdout", "-l", languages)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

// ---------------------------------------------------------------------------
// title heuristic — kept here so callers see a sensible default in `loom note`
// ---------------------------------------------------------------------------

func pdfTitle(path, content string) string {
	for _, line := range strings.SplitN(content, "\n", 12) {
		line = strings.TrimSpace(line)
		// Skip the "## Page N" headers we just generated.
		if line == "" || strings.HasPrefix(line, "## Page") {
			continue
		}
		if len(line) > 120 {
			line = line[:117] + "…"
		}
		return line
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
