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

type OpenAIConfig struct {
	Endpoint string // defaults to https://api.openai.com
	Model    string
	APIKey   string
	Timeout  time.Duration
}

type OpenAIClient struct {
	cfg OpenAIConfig
	hc  *http.Client
}

func NewOpenAI(cfg OpenAIConfig) *OpenAIClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.openai.com"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	return &OpenAIClient{
		cfg: cfg,
		hc:  &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *OpenAIClient) Name() string { return "openai:" + c.cfg.Model }

type openAIChatRequest struct {
	Model          string             `json:"model"`
	Messages       []openAIMessage    `json:"messages"`
	Temperature    *float64           `json:"temperature,omitempty"`
	MaxTokens      *int               `json:"max_tokens,omitempty"`
	ResponseFormat *openAIRespFormat  `json:"response_format,omitempty"`
	Stream         bool               `json:"stream,omitempty"`
	StreamOptions  *streamOptions     `json:"stream_options,omitempty"`
}

type streamOptions struct {
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
	Error *openAIError `json:"error,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

	msgs := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openAIMessage{Role: string(m.Role), Content: m.Content}
	}

	payload := openAIChatRequest{Model: model, Messages: msgs}
	if req.Temperature > 0 {
		t := req.Temperature
		payload.Temperature = &t
	}
	if req.MaxTokens > 0 {
		n := req.MaxTokens
		payload.MaxTokens = &n
	}
	if req.JSON {
		payload.ResponseFormat = &openAIRespFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	url := c.cfg.Endpoint + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai error: %s", out.Error.Message)
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
// `data: [DONE]` line marks end-of-stream. We optionally request a final
// `usage` frame so token counts are populated when available.
func (c *OpenAIClient) Stream(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.Model
	}

	msgs := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openAIMessage{Role: string(m.Role), Content: m.Content}
	}

	payload := openAIChatRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	if req.Temperature > 0 {
		t := req.Temperature
		payload.Temperature = &t
	}
	if req.MaxTokens > 0 {
		n := req.MaxTokens
		payload.MaxTokens = &n
	}
	if req.JSON {
		payload.ResponseFormat = &openAIRespFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	url := c.cfg.Endpoint + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	streamHC := &http.Client{}
	resp, err := streamHC.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("openai status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
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
			// SSE comment / blank line / `event:` lines — ignore.
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		var frame openAIStreamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil, fmt.Errorf("decode openai stream frame: %w", err)
		}
		if frame.Model != "" {
			lastModel = frame.Model
		}
		for _, ch := range frame.Choices {
			if ch.Delta.Content != "" {
				full.WriteString(ch.Delta.Content)
				if onChunk != nil {
					onChunk(ch.Delta.Content)
				}
			}
		}
		if frame.Usage != nil {
			promptTokens = frame.Usage.PromptTokens
			evalTokens = frame.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read openai stream: %w", err)
	}

	return &ChatResponse{
		Content:      full.String(),
		Model:        lastModel,
		PromptTokens: promptTokens,
		OutputTokens: evalTokens,
	}, nil
}
