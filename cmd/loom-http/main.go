// loom-http is a tiny HTTP server that exposes a Loom knowledge base over REST,
// so non-MCP services (e.g. a Python backend) can use Loom as a retrieval layer.
//
// It reuses the same TOML config as the loom CLI and loom-mcp; configure it with
// --config or LOOM_CONFIG. Listen address via LOOM_HTTP_ADDR (default :8080).
//
// Endpoints:
//
//	GET  /healthz                      -> "ok"
//	POST /search  {query, limit}       -> {hits:[{rel_path,title,kind,summary,keywords,content,rank}]}
//	POST /scan    {force}              -> index counts (requires a working LLM provider)
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/index"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	cfgPath := os.Getenv("LOOM_CONFIG")
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			cfgPath = strings.TrimPrefix(args[i], "--config=")
		}
	}
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		fatal("ensure db dir: %v", err)
	}
	for _, d := range cfg.NotesDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fatal("ensure notes dir %s: %v", d, err)
		}
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		fatal("open store: %v", err)
	}
	defer store.Close()

	// The LLM client is only needed for /scan (indexing). Search is LLM-free, so
	// don't make a missing/invalid provider fatal — just disable /scan.
	client, llmErr := buildLLM(cfg.LLM)
	if llmErr != nil {
		log.Printf("loom-http: LLM provider unavailable, /scan disabled: %v", llmErr)
	}

	srv := &server{store: store, client: client, cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.health)
	mux.HandleFunc("/search", srv.search)
	mux.HandleFunc("/scan", srv.scan)

	addr := os.Getenv("LOOM_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("loom-http %s listening on %s (db=%s, notes=%v)", Version, addr, cfg.DBPath, cfg.NotesDirs)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fatal("listen: %v", err)
	}
}

type server struct {
	store  *storage.Store
	client llmpkg.Client
	cfg    *config.Config
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeErr(w, http.StatusBadRequest, "query is required")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	hits, err := s.store.Search(r.Context(), req.Query, limit)
	if err != nil {
		// ErrNotFound (empty index / no query) -> empty result, not a 500
		writeJSON(w, http.StatusOK, map[string]any{"hits": []any{}})
		return
	}
	rows := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, map[string]any{
			"rel_path": h.RelPath,
			"title":    h.Title,
			"kind":     h.Kind,
			"summary":  h.Summary,
			"keywords": h.Keywords,
			"content":  h.Content,
			"rank":     h.Rank,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": rows})
}

type scanRequest struct {
	Force bool `json:"force"`
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if s.client == nil {
		writeErr(w, http.StatusServiceUnavailable, "scan unavailable: no LLM provider configured")
		return
	}
	var req scanRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	ix := index.New(s.cfg.NotesDirs, s.store, s.client)
	var (
		res *index.Result
		err error
	)
	if req.Force {
		res, err = ix.Force(r.Context())
	} else {
		res, err = ix.Scan(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added":       res.Added,
		"updated":     res.Updated,
		"removed":     res.Removed,
		"skipped":     res.Skipped,
		"failed":      res.Failed,
		"duration_ms": res.Duration.Milliseconds(),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func buildLLM(cfg config.LLMConfig) (llmpkg.Client, error) {
	switch cfg.Provider {
	case "ollama", "":
		return llmpkg.NewOllama(llmpkg.OllamaConfig{
			Endpoint: cfg.Endpoint,
			Model:    cfg.Model,
		}), nil
	case "openai":
		key := cfg.APIKey()
		if key == "" {
			return nil, fmt.Errorf("openai provider requires api_key_env to point at a non-empty env var")
		}
		return llmpkg.NewOpenAI(llmpkg.OpenAIConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	case "anthropic":
		key := cfg.APIKey()
		if key == "" {
			return nil, fmt.Errorf("anthropic provider requires api_key_env to point at a non-empty env var")
		}
		return llmpkg.NewAnthropic(llmpkg.AnthropicConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintln(os.Stderr, "loom-http:", fmt.Sprintf(format, a...))
	os.Exit(1)
}
