package extract

// vision.go — VisionExtractor indexes image files using an Ollama vision model
// (e.g. glm-ocr). When ocr_model is set in the [llm] config section, Loom
// registers this extractor so that PNG/JPG/etc. files are transcribed into
// markdown and indexed like any other document. The same OCR function is
// injected into the PDF extractor as an alternative to tesseract.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OCRFunc converts raw image bytes to extracted text (typically markdown).
// It is used both by VisionExtractor (for standalone images) and by the PDF
// extractor (for rendered PDF pages).
type OCRFunc func(ctx context.Context, imageBytes []byte) (string, error)

// imageExts is the set of file extensions handled by VisionExtractor.
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".gif":  true,
	".tiff": true,
	".tif":  true,
}

// VisionExtractor indexes image files using an Ollama vision model.
// Construct via NewVisionExtractor; the zero value is not usable.
type VisionExtractor struct {
	Endpoint string
	Model    string
	// ocrFn is a test seam — production code leaves it nil and uses
	// ollamaOCR instead.
	ocrFn OCRFunc
}

// NewVisionExtractor returns a VisionExtractor that calls the given Ollama
// endpoint and model for OCR.
func NewVisionExtractor(endpoint, model string) *VisionExtractor {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &VisionExtractor{Endpoint: strings.TrimRight(endpoint, "/"), Model: model}
}

func (v *VisionExtractor) Supports(source string) bool {
	return !IsURL(source) && imageExts[strings.ToLower(filepath.Ext(source))]
}

// Extract reads the image file and transcribes it via the vision model.
func (v *VisionExtractor) Extract(source string) (*Document, error) {
	raw, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read image %s: %w", source, err)
	}
	h := sha256.Sum256(raw)
	hashHex := hex.EncodeToString(h[:])

	abs, err := filepath.Abs(source)
	if err != nil {
		abs = source
	}
	uri := (&url.URL{Scheme: "file", Path: abs}).String()

	content, err := v.OCR(context.Background(), raw)
	if err != nil {
		return nil, fmt.Errorf("vision OCR for %s: %w", source, err)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("vision OCR returned empty content for %s", source)
	}

	base := filepath.Base(source)
	title := strings.TrimSuffix(base, filepath.Ext(base))
	// Prefer a markdown heading if the model generated one.
	for _, line := range strings.SplitN(content, "\n", 6) {
		if t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")); t != "" && strings.HasPrefix(strings.TrimSpace(line), "#") {
			title = t
			break
		}
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(source), "."))
	return &Document{
		URI:     uri,
		Kind:    ext,
		Title:   title,
		Content: content,
		Hash:    hashHex,
	}, nil
}

// OCR transcribes raw image bytes. It uses the injected test seam if set,
// otherwise calls ollamaOCR.
func (v *VisionExtractor) OCR(ctx context.Context, imageBytes []byte) (string, error) {
	if v.ocrFn != nil {
		return v.ocrFn(ctx, imageBytes)
	}
	return ollamaOCR(ctx, v.Endpoint, v.Model, imageBytes)
}

// ---------------------------------------------------------------------------
// Ollama vision API
// ---------------------------------------------------------------------------

// ollamaVisionRequest is the wire format for the /api/chat endpoint when
// images are attached to a message.
type ollamaVisionRequest struct {
	Model    string                `json:"model"`
	Messages []ollamaVisionMessage `json:"messages"`
	Stream   bool                  `json:"stream"`
}

type ollamaVisionMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"` // base64-encoded
}

type ollamaVisionResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

const ocrPrompt = "Extract and transcribe all text from this image. " +
	"Preserve document structure using markdown formatting (headings, tables, lists). " +
	"Return only the extracted content, no commentary."

// ollamaOCR calls the Ollama /api/chat endpoint with the image attached.
func ollamaOCR(ctx context.Context, endpoint, model string, imageBytes []byte) (string, error) {
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	body, err := json.Marshal(ollamaVisionRequest{
		Model: model,
		Messages: []ollamaVisionMessage{
			{Role: "user", Content: ocrPrompt, Images: []string{encoded}},
		},
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal vision request: %w", err)
	}

	hc := &http.Client{Timeout: 300 * time.Second} // vision calls can be slow
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama vision: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("ollama vision http %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	var out ollamaVisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode vision response: %w", err)
	}
	return out.Message.Content, nil
}
