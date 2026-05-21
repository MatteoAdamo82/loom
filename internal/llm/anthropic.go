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

const (
	anthropicDefaultVersion = "2023-06-01"
	anthropicDefaultMaxTok  = 2048

	// jsonNudge is appended to the system prompt when JSON mode is requested.
	// Anthropic has no native json_mode flag, so we nudge via instructions.
	jsonNudge = "\n\nReturn the response as a single, strict JSON object with no surrounding prose."
)

type AnthropicConfig struct {
	Endpoint string // defaults to https://api.anthropic.com
	Model    string
	APIKey   string
	Version  string // anthropic-version header; defaults to "2023-06-01"
	Timeout  time.Duration
}

type AnthropicClient struct {
	cfg      AnthropicConfig
	hc       *http.Client // regular calls (with Timeout)
	streamHC *http.Client // streaming calls (no Timeout — ctx governs)
	headers  map[string]string
}

func NewAnthropic(cfg AnthropicConfig) *AnthropicClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.anthropic.com"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Version == "" {
		cfg.Version = anthropicDefaultVersion
	}
	hc, streamHC := newHTTPClients(cfg.Timeout)
	headers := map[string]string{"anthropic-version": cfg.Version}
	if cfg.APIKey != "" {
		headers["x-api-key"] = cfg.APIKey
	}
	return &AnthropicClient{cfg: cfg, hc: hc, streamHC: streamHC, headers: headers}
}

func (c *AnthropicClient) Name() string { return "anthropic:" + c.cfg.Model }

// --- wire types ---------------------------------------------------------------

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

// anthropicStreamEvent is the union of SSE frame shapes. Unrecognised types
// are tolerated as no-ops (ping, content_block_start, etc.).
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// --- helpers ------------------------------------------------------------------

// convertMessages separates the Anthropic system field from user/assistant turns
// and optionally appends the JSON nudge when JSON mode is requested.
func (c *AnthropicClient) convertMessages(msgs []Message, wantJSON bool) (system string, turns []anthropicMessage) {
	for _, m := range msgs {
		if m.Role == RoleSystem {
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		}
		turns = append(turns, anthropicMessage{Role: string(m.Role), Content: m.Content})
	}
	if wantJSON {
		if system != "" {
			system += jsonNudge
		} else {
			system = strings.TrimSpace(jsonNudge)
		}
	}
	return
}

func (c *AnthropicClient) buildPayload(req ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	system, msgs := c.convertMessages(req.Messages, req.JSON)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTok
	}
	p := anthropicMessageRequest{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: maxTokens,
		Stream:    stream,
	}
	if req.Temperature > 0 {
		t := req.Temperature
		p.Temperature = &t
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	return b, nil
}

// --- Client interface ---------------------------------------------------------

func (c *AnthropicClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := c.buildPayload(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := postJSON(ctx, c.hc, c.cfg.Endpoint+"/v1/messages", c.headers, body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()

	var out anthropicMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	if len(out.Content) == 0 {
		return nil, fmt.Errorf("anthropic returned no content blocks")
	}
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

// Stream emits successive text deltas from Anthropic's SSE protocol.
// Only `content_block_delta` frames with `delta.type == "text_delta"` carry
// visible content; all other event types are either metadata or no-ops.
func (c *AnthropicClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	body, err := c.buildPayload(req, true)
	if err != nil {
		return nil, err
	}
	hdrs := make(map[string]string, len(c.headers)+1)
	for k, v := range c.headers {
		hdrs[k] = v
	}
	hdrs["Accept"] = "text/event-stream"

	resp, err := postJSON(ctx, c.streamHC, c.cfg.Endpoint+"/v1/messages", hdrs, body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()

	var acc streamAccumulator
	sc := newStreamScanner(resp.Body)
	for sc.Scan() {
		line := sc.Bytes()
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
			acc.append("", ev.Message.Model)
			acc.setTokens(ev.Message.Usage.InputTokens, 0)
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				acc.append(ev.Delta.Text, "")
				if onChunk != nil {
					onChunk(ev.Delta.Text)
				}
			}
		case "message_delta":
			acc.setTokens(0, ev.Usage.OutputTokens)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read anthropic stream: %w", err)
	}
	return acc.result(), nil
}
