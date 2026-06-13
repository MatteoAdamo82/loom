// loom-http is a tiny HTTP server that exposes a Loom knowledge base over REST,
// so non-MCP services (e.g. a Python backend) can use Loom as a retrieval layer.
//
// It reuses the same TOML config as the loom CLI and loom-mcp; configure it with
// --config or LOOM_CONFIG. Listen address via LOOM_HTTP_ADDR (default :8080).
//
// # Single corpus (default)
//
// With no LOOM_CORPUS_ROOT set, the server serves the one corpus described by
// the config file (db_path + notes_dirs). Requests omit "corpus".
//
// # Multi-corpus (optional)
//
// Set LOOM_CORPUS_ROOT=/path to serve many isolated corpora from one process —
// useful for multi-tenant hosts. Requests then carry a "corpus" name and the
// server resolves it to <root>/<corpus>/{notes,index.db}, opening (and caching)
// a separate SQLite index per corpus. Corpus names are validated to a single
// safe path segment, so one corpus can never read another's files. Requests
// that omit "corpus" still fall back to the config-file corpus.
//
// The LLM/embeddings providers are shared across all corpora (same models).
//
// Endpoints:
//
//	GET  /healthz                          -> "ok"
//	GET  /corpora                          -> {corpora:[...]}  (multi-corpus mode only)
//	POST /search  {query, limit, corpus}   -> {hits:[{rel_path,title,kind,summary,keywords,content,rank}]}
//	POST /scan    {force, corpus}          -> index counts (requires a working LLM provider)
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/index"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

// corpusNameRe restricts a corpus name to a single safe path segment: no
// slashes, no dots, no traversal. This is what isolates tenants from each other.
var corpusNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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

	// Embeddings are optional. When enabled, /search fuses BM25 + vector (hybrid)
	// and /scan stores a vector per file. If the embedder can't be built, log and
	// keep going on pure BM25 — never fatal.
	var embedder llmpkg.Embedder
	if cfg.Embeddings.Enabled {
		e, eerr := llmpkg.BuildEmbedder(cfg.Embeddings.Provider, cfg.Embeddings.Model, cfg.Embeddings.Endpoint, cfg.Embeddings.APIKey())
		if eerr != nil {
			log.Printf("loom-http: embeddings enabled but unavailable, using BM25 only: %v", eerr)
		} else {
			embedder = e
			log.Printf("loom-http: hybrid search enabled (%s)", e.Name())
		}
	}

	root := os.Getenv("LOOM_CORPUS_ROOT")
	if root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			fatal("ensure corpus root %s: %v", root, err)
		}
	}

	srv := &server{
		single:   store,
		client:   client,
		embedder: embedder,
		cfg:      cfg,
		root:     root,
		stores:   map[string]*storage.Store{},
	}
	defer srv.closeAll()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", srv.health)
	mux.HandleFunc("/corpora", srv.corpora)
	mux.HandleFunc("/search", srv.search)
	mux.HandleFunc("/scan", srv.scan)

	addr := os.Getenv("LOOM_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	mode := "single-corpus"
	if root != "" {
		mode = "multi-corpus root=" + root
	}
	log.Printf("loom-http %s listening on %s (%s, default db=%s, notes=%v)", Version, addr, mode, cfg.DBPath, cfg.NotesDirs)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fatal("listen: %v", err)
	}
}

type server struct {
	single   *storage.Store  // the config-file corpus (used when corpus is omitted)
	client   llmpkg.Client   // shared across corpora; nil → /scan disabled
	embedder llmpkg.Embedder // shared across corpora; nil → BM25 only
	cfg      *config.Config
	root     string // LOOM_CORPUS_ROOT; "" → single-corpus mode

	mu     sync.Mutex
	stores map[string]*storage.Store // corpus name → lazily-opened store
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.stores {
		_ = st.Close()
	}
}

// validateCorpus rejects anything that isn't a single safe path segment.
func validateCorpus(name string) error {
	if !corpusNameRe.MatchString(name) {
		return fmt.Errorf("invalid corpus name %q (allowed: letters, digits, '_' '-', max 64)", name)
	}
	return nil
}

// corpusPaths maps a corpus name to its db and notes paths under root.
func corpusPaths(root, corpus string) (dbPath, notesDir string) {
	base := filepath.Join(root, corpus)
	return filepath.Join(base, "index.db"), filepath.Join(base, "notes")
}

// storeFor resolves the store and notes dirs for a request's corpus. An empty
// corpus uses the config-file store (backward-compatible). A non-empty corpus
// requires multi-corpus mode and a valid name; its store is opened once and
// cached.
func (s *server) storeFor(corpus string) (*storage.Store, []string, error) {
	if corpus == "" {
		return s.single, s.cfg.NotesDirs, nil
	}
	if s.root == "" {
		return nil, nil, fmt.Errorf("corpus %q requested but multi-corpus mode is off (set LOOM_CORPUS_ROOT)", corpus)
	}
	if err := validateCorpus(corpus); err != nil {
		return nil, nil, err
	}
	dbPath, notesDir := corpusPaths(s.root, corpus)

	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.stores[corpus]; ok {
		return st, []string{notesDir}, nil
	}
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("ensure corpus notes dir: %w", err)
	}
	st, err := storage.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open corpus store: %w", err)
	}
	s.stores[corpus] = st
	return st, []string{notesDir}, nil
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// corpora lists the corpus directories under LOOM_CORPUS_ROOT (multi-corpus
// mode only). A directory counts as a corpus if it has an index.db or a notes/.
func (s *server) corpora(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	if s.root == "" {
		writeJSON(w, http.StatusOK, map[string]any{"corpora": []any{}, "multi_corpus": false})
		return
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !corpusNameRe.MatchString(e.Name()) {
			continue
		}
		base := filepath.Join(s.root, e.Name())
		if fileExists(filepath.Join(base, "index.db")) || dirExists(filepath.Join(base, "notes")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"corpora": names, "multi_corpus": true})
}

type searchRequest struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit"`
	Corpus string `json:"corpus"`
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
	store, _, err := s.storeFor(req.Corpus)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	hits, err := query.Retrieve(r.Context(), store, s.embedder, req.Query, limit)
	if err != nil {
		// empty index / no query / transient retrieval error -> empty result, not a 500
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
	Force  bool   `json:"force"`
	Corpus string `json:"corpus"`
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

	store, notesDirs, err := s.storeFor(req.Corpus)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ix := index.New(notesDirs, store, s.client)
	ix.Embedder = s.embedder
	var res *index.Result
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

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
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
