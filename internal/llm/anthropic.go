package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type AnthropicConfig struct {
	Endpoint string // defaults to https://api.anthropic.com
	Model    string
	APIKey   string
	Version  string // anthropic-version header; defaults to "2023-06-01"
	Timeout  time.Duration
}

type AnthropicClient struct {
	cfg AnthropicConfig
	hc  *http.Client
}

func NewAnthropic(cfg AnthropicConfig) *AnthropicClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.anthropic.com"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Version == "" {
		cfg.Version = "2023-06-01"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &AnthropicClient{
		cfg: cfg,
		hc:  &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *AnthropicClient) Name() string { return "anthropic:" + c.cfg.Model }

type anthropicMessageRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessageResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

const jsonNudge = "\n\nReturn the response as a single, strict JSON object with no surrounding prose."

func (c *AnthropicClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

	// Anthropic accepts a top-level system field, not a system role message.
	var system string
	var msgs []anthropicMessage
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: string(m.Role), Content: m.Content})
	}
	if req.JSON {
		// Anthropic doesn't have a JSON-mode flag; nudge via system prompt.
		if system != "" {
			system += jsonNudge
		} else {
			system = strings.TrimSpace(jsonNudge)
		}
	}

	// max_tokens is required by the API. Pick a sensible default.
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	payload := anthropicMessageRequest{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: maxTokens,
	}
	if req.Temperature > 0 {
		t := req.Temperature
		payload.Temperature = &t
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	url := c.cfg.Endpoint + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", c.cfg.Version)
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.cfg.APIKey)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	var out anthropicMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	if len(out.Content) == 0 {
		return nil, fmt.Errorf("anthropic returned no content blocks")
	}

	// Concatenate text blocks; ignore non-text blocks (tool_use, etc.) for MVP.
	var sb strings.Builder
	for _, blk := range out.Content {
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	return &ChatResponse{
		Content:      sb.String(),
		Model:        out.Model,
		PromptTokens: out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}
