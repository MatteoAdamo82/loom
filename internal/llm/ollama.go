package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	cfg      OllamaConfig
	hc       *http.Client // regular calls (with Timeout)
	streamHC *http.Client // streaming calls (no Timeout — ctx governs)
}

func NewOllama(cfg OllamaConfig) *OllamaClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	hc, streamHC := newHTTPClients(cfg.Timeout)
	return &OllamaClient{cfg: cfg, hc: hc, streamHC: streamHC}
}

func (c *OllamaClient) Name() string { return "ollama:" + c.cfg.Model }

// --- wire types ---------------------------------------------------------------

type ollamaChatRequest struct {
	Model     string         `json:"model"`
	Messages  []Message      `json:"messages"`
	Stream    bool           `json:"stream"`
	Format    string         `json:"format,omitempty"`
	Options   *ollamaOptions `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

type ollamaChatResponse struct {
	Model           string  `json:"model"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

// --- helpers ------------------------------------------------------------------

func (c *OllamaClient) buildPayload(req ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	p := ollamaChatRequest{Model: model, Messages: req.Messages, Stream: stream}
	if req.JSON {
		p.Format = "json"
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
		p.Options = opts
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}
	return b, nil
}

// --- Client interface ---------------------------------------------------------

func (c *OllamaClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := c.buildPayload(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := postJSON(ctx, c.hc, c.cfg.Endpoint+"/api/chat", nil, body)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

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

// Stream emits successive content deltas to onChunk. Ollama's streaming
// protocol is line-delimited JSON (NDJSON) over a keep-alive HTTP connection.
func (c *OllamaClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	body, err := c.buildPayload(req, true)
	if err != nil {
		return nil, err
	}
	resp, err := postJSON(ctx, c.streamHC, c.cfg.Endpoint+"/api/chat",
		map[string]string{"Accept": "application/x-ndjson"}, body)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	var acc streamAccumulator
	sc := newStreamScanner(resp.Body)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame ollamaChatResponse
		if err := json.Unmarshal(line, &frame); err != nil {
			return nil, fmt.Errorf("decode ollama stream frame: %w", err)
		}
		if chunk := frame.Message.Content; chunk != "" {
			acc.append(chunk, frame.Model)
			if onChunk != nil {
				onChunk(chunk)
			}
		}
		if frame.Done {
			acc.setTokens(frame.PromptEvalCount, frame.EvalCount)
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read ollama stream: %w", err)
	}
	return acc.result(), nil
}
