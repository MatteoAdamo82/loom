package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// streamAccumulator
// ---------------------------------------------------------------------------

func TestStreamAccumulatorAppendAndResult(t *testing.T) {
	var a streamAccumulator
	a.append("Hello ", "")
	a.append("world", "gpt-4o")
	a.append("!", "")

	r := a.result()
	if r.Content != "Hello world!" {
		t.Errorf("content = %q", r.Content)
	}
	if r.Model != "gpt-4o" {
		t.Errorf("model = %q", r.Model)
	}
}

func TestStreamAccumulatorSetTokens(t *testing.T) {
	var a streamAccumulator
	a.setTokens(10, 0)  // output zero → ignored
	a.setTokens(0, 5)   // prompt zero → ignored

	r := a.result()
	if r.PromptTokens != 10 {
		t.Errorf("prompt tokens = %d, want 10", r.PromptTokens)
	}
	if r.OutputTokens != 5 {
		t.Errorf("output tokens = %d, want 5", r.OutputTokens)
	}
}

func TestStreamAccumulatorZeroValuesIgnored(t *testing.T) {
	var a streamAccumulator
	a.setTokens(20, 8)
	a.setTokens(0, 0) // both zero — should not overwrite
	r := a.result()
	if r.PromptTokens != 20 || r.OutputTokens != 8 {
		t.Errorf("tokens were overwritten: prompt=%d output=%d", r.PromptTokens, r.OutputTokens)
	}
}

func TestStreamAccumulatorModelUpdatedOnlyWhenNonEmpty(t *testing.T) {
	var a streamAccumulator
	a.append("a", "first-model")
	a.append("b", "")         // empty model → should keep "first-model"
	a.append("c", "new-model") // non-empty → should update
	if got := a.result().Model; got != "new-model" {
		t.Errorf("model = %q, want %q", got, "new-model")
	}
}

// ---------------------------------------------------------------------------
// postJSON
// ---------------------------------------------------------------------------

func TestPostJSONSendsContentTypeHeader(t *testing.T) {
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc, _ := newHTTPClients(0)
	resp, err := postJSON(context.Background(), hc, srv.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	resp.Body.Close()

	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestPostJSONForwardsExtraHeaders(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc, _ := newHTTPClients(0)
	resp, err := postJSON(context.Background(), hc, srv.URL, map[string]string{"X-Custom": "loom"}, []byte(`{}`))
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	resp.Body.Close()

	if got != "loom" {
		t.Errorf("X-Custom header = %q, want %q", got, "loom")
	}
}

func TestPostJSONReturnsErrorOnHTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	hc, _ := newHTTPClients(0)
	_, err := postJSON(context.Background(), hc, srv.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status code: %v", err)
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error should include body: %v", err)
	}
}

func TestPostJSONContextCancellation(t *testing.T) {
	// Server blocks until the test goroutine releases it — so the cancelled
	// context should cause postJSON to return before the response arrives.
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(unblock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	hc, _ := newHTTPClients(0)
	_, err := postJSON(ctx, hc, srv.URL, nil, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ---------------------------------------------------------------------------
// newHTTPClients
// ---------------------------------------------------------------------------

func TestNewHTTPClientsRegularHasTimeout(t *testing.T) {
	regular, _ := newHTTPClients(30 * time.Second)
	if regular.Timeout != 30*time.Second {
		t.Errorf("regular timeout = %v, want 30s", regular.Timeout)
	}
}

func TestNewHTTPClientsStreamingHasNoTimeout(t *testing.T) {
	_, streaming := newHTTPClients(30 * time.Second)
	if streaming.Timeout != 0 {
		t.Errorf("streaming client must have zero Timeout, got %v", streaming.Timeout)
	}
}

func TestNewHTTPClientsDefaultTimeoutApplied(t *testing.T) {
	regular, _ := newHTTPClients(0) // zero → use defaultTimeout
	if regular.Timeout != defaultTimeout {
		t.Errorf("default timeout = %v, want %v", regular.Timeout, defaultTimeout)
	}
}

func TestNewHTTPClientsShareTransport(t *testing.T) {
	regular, streaming := newHTTPClients(0)
	if regular.Transport != streaming.Transport {
		t.Error("regular and streaming clients must share the same Transport for connection pooling")
	}
}
