// Package extract converts raw files (txt, md, pdf, html, ...) into the plain
// text that Loom stores in sources.content and feeds to chunking / LLM passes.
package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Document struct {
	URI     string
	Kind    string
	Title   string
	Content string
	Hash    string
}

type Extractor interface {
	// Supports reports whether the extractor handles the given source. The
	// source can be a local filesystem path or an http/https URL — the
	// extractor decides how to interpret it.
	Supports(source string) bool
	// Extract reads the source and returns the normalized Document.
	Extract(source string) (*Document, error)
}

// Registry resolves the right extractor for a path or URI.
type Registry struct {
	ext []Extractor
}

func NewRegistry(ext ...Extractor) *Registry {
	return &Registry{ext: ext}
}

func (r *Registry) Resolve(source string) (Extractor, error) {
	for _, e := range r.ext {
		if e.Supports(source) {
			return e, nil
		}
	}
	return nil, fmt.Errorf("no extractor for %q", source)
}

// IsURL reports whether s is an http/https URL string. Exposed so callers can
// shortcut path-only logic.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func extOf(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
}

// DefaultRegistry returns the extractors enabled by default (txt, md, pdf,
// html, http/https URLs). Callers that want a custom set can build their own
// Registry.
func DefaultRegistry() *Registry {
	return NewRegistry(Text{}, PDF{}, HTML{})
}

// ----- text (txt, md) -------------------------------------------------------

type Text struct{}

func (Text) Supports(source string) bool {
	if IsURL(source) {
		return false // URLs go through HTML
	}
	switch extOf(source) {
	case "txt", "md", "markdown", "text":
		return true
	}
	return false
}

func (t Text) Extract(path string) (*Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	content := string(b)

	title := guessTitle(path, content)
	kind := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if kind == "markdown" {
		kind = "md"
	}
	if kind == "text" {
		kind = "txt"
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	u := (&url.URL{Scheme: "file", Path: abs}).String()

	h := sha256.Sum256(b)
	return &Document{
		URI:     u,
		Kind:    kind,
		Title:   title,
		Content: content,
		Hash:    hex.EncodeToString(h[:]),
	}, nil
}

func guessTitle(path, content string) string {
	for _, line := range strings.SplitN(content, "\n", 6) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
	}
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base
}
