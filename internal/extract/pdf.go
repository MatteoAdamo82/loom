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

// minTextPerPage is the heuristic threshold below which a page is considered
// "image-only" and gets rerouted to OCR.
const minTextPerPage = 16

// PDF turns a .pdf into a Document. It runs ledongthuc/pdf first (pure Go,
// fast, no deps); if that yields too little text and `pdftoppm` + `tesseract`
// are on PATH, it falls back to OCR. There is no cache layer — the index DB
// already stores the extracted content keyed by hash.
//
// Override TESSERACT_LANGS env var (e.g. "eng+ita") to change OCR languages.
type PDF struct {
	// renderPages and ocrImage are test seams for the tesseract path.
	// Production callers leave them nil; the package supplies real
	// implementations that shell out.
	renderPages func(ctx context.Context, pdfPath, outDir string, dpi int) ([]string, error)
	ocrImage    func(ctx context.Context, imagePath, languages string) (string, error)

	// ocrVision, when non-nil, is used instead of tesseract for pages that
	// have too little selectable text. It receives the raw PNG bytes of the
	// rendered page and returns markdown text.
	ocrVision OCRFunc
}

func (PDF) Supports(source string) bool {
	return !IsURL(source) && extOf(source) == "pdf"
}

func (p PDF) Extract(source string) (*Document, error) {
	return p.ExtractContext(context.Background(), source)
}

// ExtractContext is the workhorse: hash → ledongthuc per page → OCR fallback
// per empty page → compose markdown.
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

	pages, err := readPagesWithLedongthuc(path)
	if err != nil {
		return nil, fmt.Errorf("ledongthuc: %w", err)
	}

	if p.shouldTryOCR(pages) {
		if ocred, ocrErr := p.runOCROnPages(ctx, path, pages); ocrErr == nil {
			pages = ocred
		} else if !pagesHaveText(pages) {
			return nil, fmt.Errorf("ocr fallback: %w", ocrErr)
		}
	}

	content := strings.TrimSpace(composeMarkdown(pages))
	if content == "" {
		return nil, errors.New("pdf: no extractable text (install poppler+tesseract for OCR fallback, or pre-OCR the file)")
	}

	return &Document{
		URI:     uri,
		Kind:    "pdf",
		Title:   pdfTitle(path, content),
		Content: content,
		Hash:    hashHex,
	}, nil
}

// shouldTryOCR returns true when at least one page came up too short and
// the prerequisites for the active OCR backend are available.
// - vision path (ocrVision set): only requires pdftoppm to render pages.
// - tesseract path: requires both pdftoppm and tesseract on PATH.
func (p PDF) shouldTryOCR(pages []pdfPage) bool {
	short := false
	for _, pg := range pages {
		if len(strings.TrimSpace(pg.text)) < minTextPerPage {
			short = true
			break
		}
	}
	if !short {
		return false
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return false
	}
	if p.ocrVision != nil {
		return true // vision model replaces tesseract
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		return false
	}
	return true
}

func pagesHaveText(pages []pdfPage) bool {
	for _, p := range pages {
		if strings.TrimSpace(p.text) != "" {
			return true
		}
	}
	return false
}

// runOCROnPages renders the PDF to PNGs and OCRs each "empty" page. Pages
// that already have selectable text are kept as-is.
func (p PDF) runOCROnPages(ctx context.Context, pdfPath string, pages []pdfPage) ([]pdfPage, error) {
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

	images, err := render(ctx, pdfPath, tmp, 300)
	if err != nil {
		return nil, fmt.Errorf("render pages: %w", err)
	}

	for len(pages) < len(images) {
		pages = append(pages, pdfPage{number: len(pages) + 1})
	}

	// When a vision model is configured, use it instead of tesseract for
	// each empty page — pass raw PNG bytes rather than a file path.
	visionFn := p.ocrVision

	langs := os.Getenv("TESSERACT_LANGS")
	if langs == "" {
		langs = "eng"
	}

	for i, img := range images {
		pg := &pages[i]
		if len(strings.TrimSpace(pg.text)) >= minTextPerPage {
			continue
		}

		var (
			text    string
			ocrErr  error
		)
		if visionFn != nil {
			imgBytes, readErr := os.ReadFile(img)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "loom: read rendered page %d: %v\n", i+1, readErr)
				continue
			}
			text, ocrErr = visionFn(ctx, imgBytes)
			if ocrErr != nil {
				fmt.Fprintf(os.Stderr, "loom: vision OCR failed on page %d: %v\n", i+1, ocrErr)
				continue
			}
		} else {
			text, ocrErr = ocr(ctx, img, langs)
			if ocrErr != nil {
				fmt.Fprintf(os.Stderr, "loom: tesseract failed on page %d: %v\n", i+1, ocrErr)
				continue
			}
		}
		pg.text = strings.TrimSpace(text)
		pg.viaOCR = true
	}
	return pages, nil
}

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
			pages = append(pages, pdfPage{number: i})
			continue
		}
		pages = append(pages, pdfPage{number: i, text: strings.TrimSpace(text)})
	}
	return pages, nil
}

// ---------------------------------------------------------------------------
// stage 2 backends: real subprocess calls
// ---------------------------------------------------------------------------

func renderPagesPdftoppm(ctx context.Context, pdfPath, outDir string, dpi int) ([]string, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return nil, fmt.Errorf("pdftoppm not in PATH: %w", err)
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

func ocrImageTesseract(ctx context.Context, imagePath, languages string) (string, error) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", fmt.Errorf("tesseract not in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "tesseract", imagePath, "stdout", "-l", languages)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

func pdfTitle(path, content string) string {
	for _, line := range strings.SplitN(content, "\n", 12) {
		line = strings.TrimSpace(line)
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
