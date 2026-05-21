package llm

// http.go — shared HTTP utilities for LLM clients.
//
// Every provider (Ollama, OpenAI, Anthropic) needs the same primitives:
//   - POST a JSON body and read the response
//   - Read a line-delimited stream (NDJSON or SSE)
//   - Accumulate streamed content into a ChatResponse
//
// Putting them here removes ~120 lines of identical boilerplate that was
// previously copy-pasted across three files.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout      = 120 * time.Second
	streamScannerBufMin = 64 * 1024
	streamScannerBufMax = 1024 * 1024
	errorBodyLimit      = 1024
)

// newHTTPClients returns two http.Clients for a provider:
//   - regular: with a hard Timeout for non-streaming calls
//   - streaming: without Timeout (the caller's ctx.Done() governs cancellation)
//     but sharing the same Transport so TCP/TLS connections are pooled
func newHTTPClients(timeout time.Duration) (regular, streaming *http.Client) {
	if timeout == 0 {
		timeout = defaultTimeout
	}
	tr := http.DefaultTransport
	regular = &http.Client{Timeout: timeout, Transport: tr}
	streaming = &http.Client{Transport: tr} // no Timeout — ctx handles it
	return
}

// postJSON POSTs a pre-marshalled JSON payload to url, adding the provided
// extra headers. It returns the raw HTTP response; the caller owns resp.Body.
// On HTTP ≥ 400 the body is consumed and returned as an error.
func postJSON(ctx context.Context, hc *http.Client, url string, headers map[string]string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		resp.Body.Close()
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	return resp, nil
}

// newStreamScanner returns a *bufio.Scanner sized for typical LLM streaming
// responses (64 KiB initial buffer, 1 MiB max line).
func newStreamScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, streamScannerBufMin), streamScannerBufMax)
	return sc
}

// streamAccumulator collects incremental streaming content.
type streamAccumulator struct {
	content      strings.Builder
	model        string
	promptTokens int
	outputTokens int
}

func (a *streamAccumulator) append(chunk, model string) {
	a.content.WriteString(chunk)
	if model != "" {
		a.model = model
	}
}

func (a *streamAccumulator) setTokens(prompt, output int) {
	if prompt > 0 {
		a.promptTokens = prompt
	}
	if output > 0 {
		a.outputTokens = output
	}
}

func (a *streamAccumulator) result() *ChatResponse {
	return &ChatResponse{
		Content:      a.content.String(),
		Model:        a.model,
		PromptTokens: a.promptTokens,
		OutputTokens: a.outputTokens,
	}
}
