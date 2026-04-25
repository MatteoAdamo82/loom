// app.go: bindings exposed to the Svelte frontend via Wails.
// All public methods on *App become callable from JS as window.go.main.App.<MethodName>(...).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/ingest"
	"github.com/MatteoAdamo82/loom/internal/lint"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the root Wails-bound struct.
type App struct {
	ctx      context.Context
	cfgPath  string
	mu       sync.RWMutex
	cfg      *config.Config
	store    *storage.Store
	ingestor *ingest.Pipeline
	queryEng *query.Engine
	llmName  string
	loadErr  string // human-readable bootstrap error, surfaced to UI
}

func NewApp(cfgPath string) *App {
	return &App{cfgPath: cfgPath}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.bootstrap()
}

func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *App) bootstrap() {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		a.loadErr = fmt.Sprintf("load config %s: %v", a.cfgPath, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		a.loadErr = fmt.Sprintf("ensure db dir: %v", err)
		return
	}
	store, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		a.loadErr = fmt.Sprintf("open store: %v", err)
		return
	}
	client, err := buildLLM(cfg.LLM)
	if err != nil {
		a.loadErr = fmt.Sprintf("init llm: %v", err)
		return
	}
	a.cfg = cfg
	a.store = store
	a.ingestor = ingest.NewPipeline(store, client)
	a.ingestor.ChunkCfg = ingest.ChunkConfig{
		MaxTokens: cfg.Ingest.ChunkTokens,
		Overlap:   cfg.Ingest.ChunkOverlap,
	}
	a.ingestor.MaxAnalyze = cfg.Ingest.MaxAnalyze
	a.queryEng = query.NewEngine(store, client)
	a.queryEng.Cfg = query.Config{
		BM25TopK:       cfg.Query.BM25TopK,
		GraphExpandHop: cfg.Query.GraphExpandHop,
		RerankTopK:     cfg.Query.RerankTopK,
	}
	a.llmName = client.Name()
	a.loadErr = ""
}

// ---------------------------------------------------------------------------
// view-models — small shapes returned to the UI so we don't ship the whole
// internal/storage struct surface to JS.
// ---------------------------------------------------------------------------

type StatusVM struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DBPath     string `json:"db_path,omitempty"`
	LLMName    string `json:"llm_name,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
}

type NoteSummaryVM struct {
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	Version  int      `json:"version"`
	Updated  string   `json:"updated"`
}

type NoteDetailVM struct {
	NoteSummaryVM
	Content  string   `json:"content"`
	LinksOut []LinkVM `json:"links_out"`
	LinksIn  []LinkVM `json:"links_in"`
}

type LinkVM struct {
	Kind       string `json:"kind"`
	OtherSlug  string `json:"other_slug,omitempty"`
	OtherTitle string `json:"other_title,omitempty"`
	OtherKind  string `json:"other_kind,omitempty"` // "note" | "source"
	Context    string `json:"context,omitempty"`
}

type AnswerVM struct {
	Question  string       `json:"question"`
	Answer    string       `json:"answer"`
	Expanded  []string     `json:"expanded"`
	Citations []CitationVM `json:"citations"`
}

type CitationVM struct {
	EntityRef string `json:"entity_ref"`
	Title     string `json:"title"`
	Slug      string `json:"slug,omitempty"`
}

type IngestVM struct {
	SourceID       int64    `json:"source_id"`
	Title          string   `json:"title"`
	Deduplicated   bool     `json:"deduplicated"`
	ChunksCreated  int      `json:"chunks_created"`
	NotesCreated   []string `json:"notes_created"`
	EntitiesLinked int      `json:"entities_linked"`
}

type LintReportVM struct {
	Stats    lint.Stats     `json:"stats"`
	Findings []lint.Finding `json:"findings"`
}

// SettingsVM mirrors the user-editable section of the TOML config. We only
// expose the LLM block for now; chunking / query knobs stay TOML-only since
// they're rarely tweaked from a GUI.
type SettingsVM struct {
	ConfigPath string `json:"config_path"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Endpoint   string `json:"endpoint"`
	APIKeyEnv  string `json:"api_key_env"`
	// Bootstrap reports the live state. Empty Error means the engine started
	// fine; otherwise the form should still render so the user can fix it.
	Error string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// public methods (callable from JS)
// ---------------------------------------------------------------------------

// Status reports whether bootstrap succeeded; the UI shows the error verbatim
// when OK == false instead of crashing.
func (a *App) Status() StatusVM {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.loadErr != "" {
		return StatusVM{OK: false, Error: a.loadErr, ConfigPath: a.cfgPath}
	}
	return StatusVM{
		OK:         true,
		DBPath:     a.cfg.Storage.DBPath,
		LLMName:    a.llmName,
		ConfigPath: a.cfgPath,
	}
}

// Reload re-runs bootstrap (handy after editing the TOML config).
func (a *App) Reload() StatusVM {
	a.mu.Lock()
	if a.store != nil {
		_ = a.store.Close()
		a.store = nil
	}
	a.mu.Unlock()
	a.bootstrap()
	return a.Status()
}

func (a *App) ListNotes(kind string, limit int) ([]NoteSummaryVM, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.store == nil {
		return nil, errors.New(a.loadErr)
	}
	if limit <= 0 {
		limit = 200
	}
	notes, err := a.store.ListNotes(a.ctx, kind, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]NoteSummaryVM, 0, len(notes))
	for _, n := range notes {
		out = append(out, NoteSummaryVM{
			Slug: n.Slug, Title: n.Title, Kind: n.Kind, Summary: n.Summary,
			Keywords: n.Keywords, Version: n.Version,
			Updated: n.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
	return out, nil
}

func (a *App) GetNote(slug string) (*NoteDetailVM, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.store == nil {
		return nil, errors.New(a.loadErr)
	}
	n, err := a.store.GetNoteBySlug(a.ctx, slug)
	if err != nil {
		return nil, err
	}
	out := &NoteDetailVM{
		NoteSummaryVM: NoteSummaryVM{
			Slug: n.Slug, Title: n.Title, Kind: n.Kind, Summary: n.Summary,
			Keywords: n.Keywords, Version: n.Version,
			Updated: n.UpdatedAt.Format("2006-01-02 15:04"),
		},
		Content: n.Content,
	}

	if outLinks, err := a.store.LinksFromNote(a.ctx, n.ID); err == nil {
		for _, l := range outLinks {
			out.LinksOut = append(out.LinksOut, a.linkVM(l, true))
		}
	}
	if inLinks, err := a.store.LinksToNote(a.ctx, n.ID); err == nil {
		for _, l := range inLinks {
			out.LinksIn = append(out.LinksIn, a.linkVM(l, false))
		}
	}
	return out, nil
}

// Event names emitted to the frontend during a streaming Ask. We keep the
// wire format minimal — the JS side only needs to know the chunk and when
// streaming starts/ends.
const (
	eventAnswerStart = "loom:answer:start"
	eventAnswerChunk = "loom:answer:chunk"
	eventAnswerEnd   = "loom:answer:end"
)

// Ask runs a query through the engine and streams the synthesised answer to
// the frontend via Wails events. The returned AnswerVM is resolved once the
// full pipeline finishes; the live UI updates from the chunk events in the
// meantime. format follows the same enum as the CLI: markdown | marp | text.
func (a *App) Ask(question, format string) (*AnswerVM, error) {
	a.mu.RLock()
	eng := a.queryEng
	store := a.store
	cfgQuery := query.Config{}
	if a.cfg != nil {
		cfgQuery = query.Config{
			BM25TopK:       a.cfg.Query.BM25TopK,
			GraphExpandHop: a.cfg.Query.GraphExpandHop,
			RerankTopK:     a.cfg.Query.RerankTopK,
		}
	}
	ctx := a.ctx
	a.mu.RUnlock()
	if eng == nil {
		return nil, errors.New(a.loadErr)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("question is empty")
	}

	// Build a per-call engine snapshot so concurrent Asks (unlikely from the
	// UI but cheap to defend against) can't trample each other's settings.
	perCall := *eng
	perCall.Cfg = cfgQuery
	perCall.Cfg.Format = query.ParseFormat(format)
	perCall.OnSynthesisChunk = func(s string) {
		wailsruntime.EventsEmit(ctx, eventAnswerChunk, s)
	}

	wailsruntime.EventsEmit(ctx, eventAnswerStart, question)
	ans, err := perCall.Run(ctx, question)
	wailsruntime.EventsEmit(ctx, eventAnswerEnd)
	if err != nil {
		return nil, err
	}

	cites := make([]CitationVM, 0, len(ans.Citations))
	for _, c := range ans.Citations {
		slug := ""
		if strings.HasPrefix(c.EntityRef, "note:") && store != nil {
			var id int64
			if _, err := fmt.Sscanf(c.EntityRef, "note:%d", &id); err == nil {
				if n, err := store.GetNote(ctx, id); err == nil {
					slug = n.Slug
				}
			}
		}
		cites = append(cites, CitationVM{EntityRef: c.EntityRef, Title: c.Title, Slug: slug})
	}
	return &AnswerVM{
		Question:  question,
		Answer:    ans.Content,
		Expanded:  ans.Expanded,
		Citations: cites,
	}, nil
}

// PickAndIngest opens the native file picker and ingests the chosen file.
// Returns nil result + nil error when the user cancels.
func (a *App) PickAndIngest() (*IngestVM, error) {
	a.mu.RLock()
	pipeline := a.ingestor
	ctx := a.ctx
	a.mu.RUnlock()
	if pipeline == nil {
		return nil, errors.New(a.loadErr)
	}
	path, err := wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title: "Select a document to ingest",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Documents (txt, md, pdf, html)", Pattern: "*.txt;*.md;*.markdown;*.pdf;*.html;*.htm"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	res, err := pipeline.Ingest(ctx, path)
	if err != nil {
		return nil, err
	}
	return ingestResultVM(res), nil
}

// IngestPath ingests a specific file path (used for drag-and-drop).
func (a *App) IngestPath(path string) (*IngestVM, error) {
	a.mu.RLock()
	pipeline := a.ingestor
	ctx := a.ctx
	a.mu.RUnlock()
	if pipeline == nil {
		return nil, errors.New(a.loadErr)
	}
	res, err := pipeline.Ingest(ctx, path)
	if err != nil {
		return nil, err
	}
	return ingestResultVM(res), nil
}

// Settings returns the on-disk LLM configuration so the UI can pre-populate
// the form. It works even when the engine failed to bootstrap so the user can
// always recover from a bad config (e.g. a missing model name).
func (a *App) Settings() (*SettingsVM, error) {
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return nil, err
	}
	a.mu.RLock()
	bootErr := a.loadErr
	a.mu.RUnlock()
	return &SettingsVM{
		ConfigPath: a.cfgPath,
		Provider:   cfg.LLM.Provider,
		Model:      cfg.LLM.Model,
		Endpoint:   cfg.LLM.Endpoint,
		APIKeyEnv:  cfg.LLM.APIKeyEnv,
		Error:      bootErr,
	}, nil
}

// SaveSettings rewrites the LLM block of the TOML config and re-runs
// bootstrap. The caller gets back the new live status.
func (a *App) SaveSettings(provider, model, endpoint, apiKeyEnv string) (StatusVM, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "ollama", "openai", "anthropic":
	default:
		return StatusVM{}, fmt.Errorf("unsupported provider %q (want ollama|openai|anthropic)", provider)
	}
	if strings.TrimSpace(model) == "" {
		return StatusVM{}, errors.New("model is required")
	}

	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return StatusVM{}, err
	}
	cfg.LLM.Provider = provider
	cfg.LLM.Model = strings.TrimSpace(model)
	cfg.LLM.Endpoint = strings.TrimSpace(endpoint)
	cfg.LLM.APIKeyEnv = strings.TrimSpace(apiKeyEnv)

	if err := config.Save(cfg, a.cfgPath); err != nil {
		return StatusVM{}, err
	}
	return a.Reload(), nil
}

// OllamaModel is one entry from /api/tags.
type OllamaModel struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ListOllamaModels queries the configured Ollama endpoint (or the explicit
// endpoint argument) for the locally available models. Used by the GUI to
// pre-populate a dropdown so users don't have to remember model names.
// The error is surfaced to the UI so the user can see why the dropdown is
// empty (typical causes: Ollama not running, wrong endpoint, firewall).
func (a *App) ListOllamaModels(endpoint string) ([]OllamaModel, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		a.mu.RLock()
		if a.cfg != nil {
			endpoint = a.cfg.LLM.Endpoint
		}
		a.mu.RUnlock()
	}
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	endpoint = strings.TrimRight(endpoint, "/")

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s/api/tags: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama %s/api/tags returned status %d", endpoint, resp.StatusCode)
	}

	var body struct {
		Models []OllamaModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode /api/tags response: %w", err)
	}
	sort.Slice(body.Models, func(i, j int) bool {
		return body.Models[i].Name < body.Models[j].Name
	})
	return body.Models, nil
}

func (a *App) Lint() (*LintReportVM, error) {
	a.mu.RLock()
	store := a.store
	a.mu.RUnlock()
	if store == nil {
		return nil, errors.New(a.loadErr)
	}
	report, err := lint.Run(a.ctx, store, lint.Config{})
	if err != nil {
		return nil, err
	}
	lint.SortFindings(report.Findings)
	return &LintReportVM{Stats: report.Stats, Findings: report.Findings}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (a *App) linkVM(l *storage.Link, outbound bool) LinkVM {
	v := LinkVM{Kind: string(l.Kind), Context: l.Context}
	target := func(noteID *int64, sourceID *int64) {
		if noteID != nil {
			if n, err := a.store.GetNote(a.ctx, *noteID); err == nil {
				v.OtherSlug = n.Slug
				v.OtherTitle = n.Title
				v.OtherKind = "note"
			}
		} else if sourceID != nil {
			if s, err := a.store.GetSource(a.ctx, *sourceID); err == nil {
				v.OtherTitle = s.Title
				v.OtherKind = "source"
				v.OtherSlug = fmt.Sprintf("source:%d", s.ID)
			}
		}
	}
	if outbound {
		target(l.ToNoteID, l.ToSourceID)
	} else {
		target(l.FromNoteID, l.FromSourceID)
	}
	return v
}

func ingestResultVM(res *ingest.Result) *IngestVM {
	slugs := make([]string, 0, len(res.NotesCreated))
	for _, n := range res.NotesCreated {
		slugs = append(slugs, n.Slug)
	}
	return &IngestVM{
		SourceID:       res.Source.ID,
		Title:          res.Source.Title,
		Deduplicated:   res.Deduplicated,
		ChunksCreated:  res.ChunksCreated,
		NotesCreated:   slugs,
		EntitiesLinked: res.EntitiesLinked,
	}
}

func buildLLM(cfg config.LLMConfig) (llmpkg.Client, error) {
	switch cfg.Provider {
	case "ollama", "":
		return llmpkg.NewOllama(llmpkg.OllamaConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model,
		}), nil
	case "openai":
		key := cfg.APIKey()
		if key == "" {
			return nil, errors.New("openai provider requires api_key_env")
		}
		return llmpkg.NewOpenAI(llmpkg.OpenAIConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	case "anthropic":
		key := cfg.APIKey()
		if key == "" {
			return nil, errors.New("anthropic provider requires api_key_env")
		}
		return llmpkg.NewAnthropic(llmpkg.AnthropicConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}
