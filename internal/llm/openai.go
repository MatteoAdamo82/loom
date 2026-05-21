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

type OpenAIConfig struct {
	Endpoint string // defaults to https://api.openai.com
	Model    string
	APIKey   string
	Timeout  time.Duration
}

type OpenAIClient struct {
	cfg      OpenAIConfig
	hc       *http.Client // regular calls (with Timeout)
	streamHC *http.Client // streaming calls (no Timeout — ctx governs)
	headers  map[string]string
}

func NewOpenAI(cfg OpenAIConfig) *OpenAIClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.openai.com"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	hc, streamHC := newHTTPClients(cfg.Timeout)
	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	return &OpenAIClient{cfg: cfg, hc: hc, streamHC: streamHC, headers: headers}
}

func (c *OpenAIClient) Name() string { return "openai:" + c.cfg.Model }

// --- wire types ---------------------------------------------------------------

type openAIChatRequest struct {
	Model          string            `json:"model"`
	Messages       []openAIMessage   `json:"messages"`
	Temperature    *float64          `json:"temperature,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	ResponseFormat *openAIRespFormat `json:"response_format,omitempty"`
	Stream         bool              `json:"stream,omitempty"`
	StreamOptions  *openAIStreamOpts `json:"stream_options,omitempty"`
}

type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIStreamFrame struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta openAIMessage `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRespFormat struct {
	Type string `json:"type"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *openAIAPIError `json:"error,omitempty"`
}

type openAIAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// --- helpers ------------------------------------------------------------------

func (c *OpenAIClient) convertMessages(msgs []Message) []openAIMessage {
	out := make([]openAIMessage, len(msgs))
	for i, m := range msgs {
		out[i] = openAIMessage{Role: string(m.Role), Content: m.Content}
	}
	return out
}

func (c *OpenAIClient) buildPayload(req ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}
	p := openAIChatRequest{Model: model, Messages: c.convertMessages(req.Messages)}
	if req.Temperature > 0 {
		t := req.Temperature
		p.Temperature = &t
	}
	if req.MaxTokens > 0 {
		n := req.MaxTokens
		p.MaxTokens = &n
	}
	if req.JSON {
		p.ResponseFormat = &openAIRespFormat{Type: "json_object"}
	}
	if stream {
		p.Stream = true
		p.StreamOptions = &openAIStreamOpts{IncludeUsage: true}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}
	return b, nil
}

// --- Client interface ---------------------------------------------------------

func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := c.buildPayload(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := postJSON(ctx, c.hc, c.cfg.Endpoint+"/v1/chat/completions", c.headers, body)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai api error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}
	return &ChatResponse{
		Content:      out.Choices[0].Message.Content,
		Model:        out.Model,
		PromptTokens: out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}

// Stream emits successive content deltas via Server-Sent Events. The terminal
// `data: [DONE]` line marks end-of-stream. A final `usage` frame populates
// token counts when the API supports it.
func (c *OpenAIClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	body, err := c.buildPayload(req, true)
	if err != nil {
		return nil, err
	}
	hdrs := make(map[string]string, len(c.headers)+1)
	for k, v := range c.headers {
		hdrs[k] = v
	}
	hdrs["Accept"] = "text/event-stream"

	resp, err := postJSON(ctx, c.streamHC, c.cfg.Endpoint+"/v1/chat/completions", hdrs, body)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	var acc streamAccumulator
	sc := newStreamScanner(resp.Body)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // SSE comments, blank lines, event: lines
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			if bytes.Equal(data, []byte("[DONE]")) {
				break
			}
			continue
		}
		var frame openAIStreamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil, fmt.Errorf("decode openai stream frame: %w", err)
		}
		for _, ch := range frame.Choices {
			if chunk := ch.Delta.Content; chunk != "" {
				acc.append(chunk, frame.Model)
				if onChunk != nil {
					onChunk(chunk)
				}
			}
		}
		if frame.Usage != nil {
			acc.setTokens(frame.Usage.PromptTokens, frame.Usage.CompletionTokens)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read openai stream: %w", err)
	}
	return acc.result(), nil
}
