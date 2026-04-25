package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"
)

// httpFetchTimeout caps a single URL ingest. Long enough for slow blogs, short
// enough that a hung server can't lock the CLI forever.
const httpFetchTimeout = 30 * time.Second

// httpFetchMaxBytes is a hard limit on response body size. 10 MiB is well above
// the typical article and well below memory pressure for an in-memory string
// pass to readability.
const httpFetchMaxBytes = 10 * 1024 * 1024

// HTML uses go-readability (Mozilla Readability port) to strip nav, ads, and
// chrome from HTML pages, returning just the article body as plain text. It
// reads either local .html files or fetches http/https URLs directly.
type HTML struct {
	// Client is the HTTP client used for URL ingest. nil means "use the
	// package default with httpFetchTimeout".
	Client *http.Client
	// UserAgent overrides the User-Agent header sent on URL fetches.
	UserAgent string
}

func (HTML) Supports(source string) bool {
	if IsURL(source) {
		return true
	}
	switch extOf(source) {
	case "html", "htm":
		return true
	}
	return false
}

func (h HTML) Extract(source string) (*Document, error) {
	if IsURL(source) {
		return h.extractURL(source)
	}
	return h.extractFile(source)
}

func (h HTML) extractFile(path string) (*Document, error) {
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
	return h.parse(raw, pageURL, path, "html")
}

func (h HTML) extractURL(src string) (*Document, error) {
	pageURL, err := url.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: httpFetchTimeout}
	}

	req, err := http.NewRequest(http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	ua := h.UserAgent
	if ua == "" {
		ua = "loom/0 (+https://github.com/MatteoAdamo82/loom)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get %s: %w", src, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http status %d for %s: %s",
			resp.StatusCode, src, strings.TrimSpace(string(body)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpFetchMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(raw)) > httpFetchMaxBytes {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", src, httpFetchMaxBytes)
	}

	// Use the URL after redirects as the canonical source: if the user typed a
	// shortlink, we want the resolved URL in the database for dedup.
	finalURL := pageURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}
	return h.parse(raw, finalURL, finalURL.Path, "html")
}

// parse runs readability on a raw HTML byte slice and packages a Document.
// titleHint and kind let the caller fall back to a sensible title when
// readability can't extract one (e.g. some bare landing pages).
func (h HTML) parse(raw []byte, pageURL *url.URL, titleHint string, kind string) (*Document, error) {
	article, err := readability.FromReader(strings.NewReader(string(raw)), pageURL)
	if err != nil {
		return nil, fmt.Errorf("readability: %w", err)
	}
	content := strings.TrimSpace(article.TextContent)
	title := strings.TrimSpace(article.Title)
	if title == "" {
		base := filepath.Base(titleHint)
		title = strings.TrimSuffix(base, filepath.Ext(base))
		if title == "" || title == "." || title == "/" {
			title = pageURL.Host
		}
	}

	hash := sha256.Sum256(raw)
	return &Document{
		URI:     pageURL.String(),
		Kind:    kind,
		Title:   title,
		Content: content,
		Hash:    hex.EncodeToString(hash[:]),
	}, nil
}
