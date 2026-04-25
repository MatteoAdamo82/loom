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
	Stream      bool               `json:"stream,omitempty"`
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

// anthropicStreamEvent is the union of frame shapes Anthropic sends. We only
// care about a few fields; ignored frames (e.g. ping, content_block_start)
// are tolerated as no-ops.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type         string `json:"type"`
		Text         string `json:"text"`
		StopReason   string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Stream emits successive text deltas from Anthropic's SSE protocol. We watch
// `content_block_delta` frames whose `delta.type == "text_delta"`.
func (c *AnthropicClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

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
		if system != "" {
			system += jsonNudge
		} else {
			system = strings.TrimSpace(jsonNudge)
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	payload := anthropicMessageRequest{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: maxTokens,
		Stream:    true,
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
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", c.cfg.Version)
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.cfg.APIKey)
	}

	streamHC := &http.Client{}
	resp, err := streamHC.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
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
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		var ev anthropicStreamEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil, fmt.Errorf("decode anthropic stream frame: %w", err)
		}
		switch ev.Type {
		case "message_start":
			lastModel = ev.Message.Model
			promptTokens = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				full.WriteString(ev.Delta.Text)
				if onChunk != nil {
					onChunk(ev.Delta.Text)
				}
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				evalTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			// Will return after scanner exits.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read anthropic stream: %w", err)
	}

	return &ChatResponse{
		Content:      full.String(),
		Model:        lastModel,
		PromptTokens: promptTokens,
		OutputTokens: evalTokens,
	}, nil
}
