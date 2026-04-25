package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OllamaConfig struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
}

type OllamaClient struct {
	cfg OllamaConfig
	hc  *http.Client
}

func NewOllama(cfg OllamaConfig) *OllamaClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &OllamaClient{
		cfg: cfg,
		hc:  &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *OllamaClient) Name() string { return "ollama:" + c.cfg.Model }

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format,omitempty"`
	Options  *ollamaOptions  `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

type ollamaChatResponse struct {
	Model              string  `json:"model"`
	Message            Message `json:"message"`
	Done               bool    `json:"done"`
	PromptEvalCount    int     `json:"prompt_eval_count"`
	EvalCount          int     `json:"eval_count"`
}

func (c *OllamaClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

	payload := ollamaChatRequest{
		Model:    model,
		Messages: req.Messages,
		Stream:   false,
	}
	if req.JSON {
		payload.Format = "json"
	}
	if req.Temperature > 0 || req.MaxTokens > 0 {
		opts := &ollamaOptions{}
		if req.Temperature > 0 {
			t := req.Temperature
			opts.Temperature = &t
		}
		if req.MaxTokens > 0 {
			n := req.MaxTokens
			opts.NumPredict = &n
		}
		payload.Options = opts
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	url := c.cfg.Endpoint + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	return &ChatResponse{
		Content:      out.Message.Content,
		Model:        out.Model,
		PromptTokens: out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}, nil
}

// Stream emits successive content deltas to onChunk and returns the full
// concatenated answer once Ollama signals "done":true. Ollama's streaming
// protocol is line-delimited JSON over keep-alive HTTP.
func (c *OllamaClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

	payload := ollamaChatRequest{
		Model:    model,
		Messages: req.Messages,
		Stream:   true,
	}
	if req.JSON {
		payload.Format = "json"
	}
	if req.Temperature > 0 || req.MaxTokens > 0 {
		opts := &ollamaOptions{}
		if req.Temperature > 0 {
			t := req.Temperature
			opts.Temperature = &t
		}
		if req.MaxTokens > 0 {
			n := req.MaxTokens
			opts.NumPredict = &n
		}
		payload.Options = opts
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	url := c.cfg.Endpoint + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	// Streaming requests need an HTTP client without a global Timeout; the
	// per-call ctx (with deadline if any) governs cancellation.
	streamHC := &http.Client{}
	resp, err := streamHC.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	var (
		full         strings.Builder
		lastModel    string
		promptTokens int
		evalTokens   int
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame ollamaChatResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			return nil, fmt.Errorf("decode ollama stream frame: %w", err)
		}
		if frame.Model != "" {
			lastModel = frame.Model
		}
		if frame.Message.Content != "" {
			full.WriteString(frame.Message.Content)
			if onChunk != nil {
				onChunk(frame.Message.Content)
			}
		}
		if frame.Done {
			promptTokens = frame.PromptEvalCount
			evalTokens = frame.EvalCount
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ollama stream: %w", err)
	}

	return &ChatResponse{
		Content:      full.String(),
		Model:        lastModel,
		PromptTokens: promptTokens,
		OutputTokens: evalTokens,
	}, nil
}
