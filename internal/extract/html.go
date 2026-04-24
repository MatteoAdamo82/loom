package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-shiori/go-readability"
)

// HTML uses go-readability (Mozilla Readability port) to strip nav, ads, and
// chrome from HTML pages, returning just the article body as plain text.
type HTML struct{}

func (HTML) Supports(ext string) bool {
	switch ext {
	case "html", "htm":
		return true
	}
	return false
}

func (h HTML) Extract(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	pageURL, err := url.Parse((&url.URL{Scheme: "file", Path: abs}).String())
	if err != nil {
		return nil, err
	}

	article, err := readability.FromReader(strings.NewReader(string(raw)), pageURL)
	if err != nil {
		return nil, fmt.Errorf("readability: %w", err)
	}

	content := strings.TrimSpace(article.TextContent)
	title := strings.TrimSpace(article.Title)
	if title == "" {
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}

	hash := sha256.Sum256(raw)
	return &Document{
		URI:     pageURL.String(),
		Kind:    "html",
		Title:   title,
		Content: content,
		Hash:    hex.EncodeToString(hash[:]),
	}, nil
}
