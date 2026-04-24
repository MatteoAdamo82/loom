package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicChatSuccess(t *testing.T) {
	var captured anthropicMessageRequest
	var apiKey, version string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		apiKey = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode captured: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_123",
			"model": "claude-sonnet-4-6",
			"content": []map[string]string{
				{"type": "text", "text": `{"ok":true}`},
			},
			"usage": map[string]int{"input_tokens": 22, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	c := NewAnthropic(AnthropicConfig{
		Endpoint: srv.URL,
		Model:    "claude-sonnet-4-6",
		APIKey:   "k-test",
	})
	out, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are helpful"},
			{Role: RoleSystem, Content: "be concise"},
			{Role: RoleUser, Content: "ping"},
		},
		JSON:        true,
		Temperature: 0.4,
		MaxTokens:   512,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	if out.Content != `{"ok":true}` {
		t.Errorf("content = %q", out.Content)
	}
	if out.PromptTokens != 22 || out.OutputTokens != 5 {
		t.Errorf("token counts = %d / %d", out.PromptTokens, out.OutputTokens)
	}
	if apiKey != "k-test" {
		t.Errorf("x-api-key = %q", apiKey)
	}
	if version != "2023-06-01" {
		t.Errorf("anthropic-version = %q", version)
	}
	// system messages were merged and the JSON nudge was appended
	if !strings.Contains(captured.System, "you are helpful") || !strings.Contains(captured.System, "be concise") {
		t.Errorf("system not merged: %q", captured.System)
	}
	if !strings.Contains(captured.System, "single, strict JSON object") {
		t.Errorf("JSON nudge missing: %q", captured.System)
	}
	// only the user message should remain in messages[]
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" {
		t.Errorf("messages should hold only the user turn: %+v", captured.Messages)
	}
	if captured.MaxTokens != 512 {
		t.Errorf("max_tokens = %d", captured.MaxTokens)
	}
}

func TestAnthropicChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"not_found_error","message":"unknown model"}}`,
			http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewAnthropic(AnthropicConfig{Endpoint: srv.URL, Model: "nope"})
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on 404 status")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error missing body: %v", err)
	}
}

func TestAnthropicConcatenatesTextBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "claude",
			"content": []map[string]string{
				{"type": "text", "text": "alpha "},
				{"type": "tool_use", "text": "ignored"},
				{"type": "text", "text": "beta"},
			},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()

	c := NewAnthropic(AnthropicConfig{Endpoint: srv.URL, Model: "claude"})
	out, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "alpha beta" {
		t.Errorf("text block concatenation failed: %q", out.Content)
	}
}

func TestAnthropicDefaultMaxTokens(t *testing.T) {
	var captured anthropicMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": "ok"}},
		})
	}))
	defer srv.Close()
	c := NewAnthropic(AnthropicConfig{Endpoint: srv.URL, Model: "x"})
	_, _ = c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	if captured.MaxTokens <= 0 {
		t.Errorf("max_tokens must be set even without explicit value, got %d", captured.MaxTokens)
	}
}
