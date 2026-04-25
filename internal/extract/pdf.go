package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDF extracts plain text from a .pdf file. We use the pure-Go ledongthuc/pdf
// reader so the binary stays CGo-free; complex PDFs (heavy CMap, scanned
// pages) may yield imperfect text — for those we recommend pre-converting
// outside of Loom for now.
type PDF struct{}

func (PDF) Supports(source string) bool {
	return !IsURL(source) && extOf(source) == "pdf"
}

func (p PDF) Extract(path string) (*Document, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	defer f.Close()

	var sb strings.Builder
	totalPages := r.NumPage()
	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		txt, err := page.GetPlainText(nil)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i, err)
		}
		sb.WriteString(strings.TrimRight(txt, " \n\r\t"))
		sb.WriteString("\n\n")
	}
	content := strings.TrimSpace(sb.String())

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	uri := (&url.URL{Scheme: "file", Path: abs}).String()

	// Hash the raw bytes, not the extracted text, so two runs of the
	// extractor on the same file always dedup even if extraction tweaks change
	// the rendered text.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(raw)

	title := pdfTitle(path, content)
	return &Document{
		URI:     uri,
		Kind:    "pdf",
		Title:   title,
		Content: content,
		Hash:    hex.EncodeToString(h[:]),
	}, nil
}

func pdfTitle(path, content string) string {
	for _, line := range strings.SplitN(content, "\n", 8) {
		line = strings.TrimSpace(line)
		if line == "" {
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
