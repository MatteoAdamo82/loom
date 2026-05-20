// app.go: bindings exposed to the Svelte frontend via Wails.
// Public methods on *App become callable from JS.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/index"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx     context.Context
	cfgPath string

	mu      sync.RWMutex
	cfg     *config.Config
	store   *storage.Store
	llm     llmpkg.Client
	llmName string
	loadErr string

	scanMu     sync.Mutex
	scanCancel context.CancelFunc
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

	if a.store != nil {
		_ = a.store.Close()
		a.store = nil
	}

	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		a.loadErr = fmt.Sprintf("load config %s: %v", a.cfgPath, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		a.loadErr = fmt.Sprintf("ensure db dir: %v", err)
		return
	}
	if err := os.MkdirAll(cfg.NotesDir, 0o755); err != nil {
		a.loadErr = fmt.Sprintf("ensure notes dir: %v", err)
		return
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		a.loadErr = fmt.Sprintf("open store: %v", err)
		return
	}
	client, err := buildLLM(cfg.LLM)
	if err != nil {
		a.loadErr = fmt.Sprintf("init llm: %v", err)
		_ = store.Close()
		return
	}
	a.cfg = cfg
	a.store = store
	a.llm = client
	a.llmName = client.Name()
	a.loadErr = ""
}

// ---------------------------------------------------------------------------
// view-models
// ---------------------------------------------------------------------------

type StatusVM struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	NotesDir   string `json:"notes_dir,omitempty"`
	DBPath     string `json:"db_path,omitempty"`
	LLMName    string `json:"llm_name,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	FileCount  int    `json:"file_count"`
}

type FileVM struct {
	RelPath  string   `json:"rel_path"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	IndexedAt string  `json:"indexed_at"`
}

type ScanResultVM struct {
	Added      int      `json:"added"`
	Updated    int      `json:"updated"`
	Moved      int      `json:"moved"`
	Removed    int      `json:"removed"`
	Skipped    int      `json:"skipped"`
	Failed     int      `json:"failed"`
	Errors     []string `json:"errors,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

type AskResultVM struct {
	Answer    string             `json:"answer"`
	Citations []query.Citation   `json:"citations"`
	Used      []query.Citation   `json:"used"`
}

// ---------------------------------------------------------------------------
// bindings
// ---------------------------------------------------------------------------

// Status returns the current backend health: where things live, the LLM in
// use, and how many files are indexed. The UI calls this on mount.
func (a *App) Status() StatusVM {
	a.mu.RLock()
	defer a.mu.RUnlock()

	vm := StatusVM{ConfigPath: a.cfgPath}
	if a.loadErr != "" {
		vm.Error = a.loadErr
		return vm
	}
	count, err := a.store.Count(a.ctx)
	if err != nil {
		vm.Error = err.Error()
		return vm
	}
	vm.OK = true
	vm.NotesDir = a.cfg.NotesDir
	vm.DBPath = a.cfg.DBPath
	vm.LLMName = a.llmName
	vm.FileCount = count
	return vm
}

// Settings returns the current config values for the settings modal.
func (a *App) Settings() *config.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return config.Default()
	}
	cp := *a.cfg
	return &cp
}

// SaveSettings writes the modified config to disk and re-bootstraps. Returns
// the resulting Status so the UI can render the new state in one round-trip.
func (a *App) SaveSettings(cfg config.Config) StatusVM {
	if err := config.Save(&cfg, a.cfgPath); err != nil {
		return StatusVM{Error: err.Error(), ConfigPath: a.cfgPath}
	}
	a.bootstrap()
	return a.Status()
}

// ListFiles returns every indexed file (sorted by rel_path).
func (a *App) ListFiles() []FileVM {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.store == nil {
		return nil
	}
	files, err := a.store.List(a.ctx)
	if err != nil {
		return nil
	}
	out := make([]FileVM, 0, len(files))
	for _, f := range files {
		out = append(out, FileVM{
			RelPath:   f.RelPath,
			Title:     f.Title,
			Kind:      f.Kind,
			Summary:   f.Summary,
			Keywords:  f.Keywords,
			IndexedAt: f.IndexedAt.Format("2006-01-02 15:04"),
		})
	}
	return out
}

// GetFileContent returns the raw extracted content of a file for the viewer.
func (a *App) GetFileContent(relPath string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.store == nil {
		return "", fmt.Errorf("not initialised")
	}
	f, err := a.store.GetByRelPath(a.ctx, relPath)
	if err != nil {
		return "", err
	}
	return f.Content, nil
}

// Rescan walks the notes folder and updates the index. Emits Wails events
// "scan:progress" and "scan:done" so the UI can show progress.
func (a *App) Rescan(force bool) ScanResultVM {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()

	a.mu.RLock()
	if a.store == nil || a.llm == nil {
		a.mu.RUnlock()
		return ScanResultVM{Errors: []string{"backend not initialised — check settings"}}
	}
	cfg := a.cfg
	store := a.store
	lc := a.llm
	a.mu.RUnlock()

	ctx, cancel := context.WithCancel(a.ctx)
	a.scanCancel = cancel
	defer func() { a.scanCancel = nil }()

	ix := index.New(cfg.NotesDir, store, lc)
	ix.OnEvent = func(e index.Event) {
		wailsruntime.EventsEmit(a.ctx, "scan:progress", map[string]any{
			"phase":    e.Phase,
			"rel_path": e.RelPath,
			"index":    e.Index,
			"total":    e.Total,
			"error":    errString(e.Err),
		})
	}

	var (
		res *index.Result
		err error
	)
	if force {
		res, err = ix.Force(ctx)
	} else {
		res, err = ix.Scan(ctx)
	}

	vm := ScanResultVM{}
	if err != nil {
		vm.Errors = append(vm.Errors, err.Error())
		wailsruntime.EventsEmit(a.ctx, "scan:done", vm)
		return vm
	}
	vm.Added = res.Added
	vm.Updated = res.Updated
	vm.Moved = res.Moved
	vm.Removed = res.Removed
	vm.Skipped = res.Skipped
	vm.Failed = res.Failed
	vm.DurationMs = res.Duration.Milliseconds()
	for _, fe := range res.Errors {
		vm.Errors = append(vm.Errors, fmt.Sprintf("%s: %v", fe.RelPath, fe.Err))
	}
	wailsruntime.EventsEmit(a.ctx, "scan:done", vm)
	return vm
}

// CancelScan aborts an in-flight Rescan, if any.
func (a *App) CancelScan() {
	if a.scanCancel != nil {
		a.scanCancel()
	}
}

// Ask streams an answer back via the "ask:chunk" Wails event. The returned
// AskResultVM is the final assembled answer plus citations.
func (a *App) Ask(question string) AskResultVM {
	a.mu.RLock()
	if a.store == nil || a.llm == nil {
		a.mu.RUnlock()
		return AskResultVM{Answer: "Backend non inizializzato — controlla le impostazioni."}
	}
	store := a.store
	lc := a.llm
	a.mu.RUnlock()

	res, err := query.Stream(a.ctx, store, lc, question, query.DefaultTopK, func(chunk string) {
		wailsruntime.EventsEmit(a.ctx, "ask:chunk", chunk)
	})
	if err != nil {
		msg := fmt.Sprintf("Errore: %v", err)
		wailsruntime.EventsEmit(a.ctx, "ask:chunk", msg)
		return AskResultVM{Answer: msg}
	}
	return AskResultVM{
		Answer:    res.Answer,
		Citations: res.Citations,
		Used:      res.Used,
	}
}

// OpenNotesDir asks the host OS to open the configured notes folder.
func (a *App) OpenNotesDir() error {
	a.mu.RLock()
	dir := ""
	if a.cfg != nil {
		dir = a.cfg.NotesDir
	}
	a.mu.RUnlock()
	if dir == "" {
		return fmt.Errorf("notes dir not configured")
	}
	wailsruntime.BrowserOpenURL(a.ctx, "file://"+dir)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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
			return nil, fmt.Errorf("openai requires api_key_env to point at a non-empty env var")
		}
		return llmpkg.NewOpenAI(llmpkg.OpenAIConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	case "anthropic":
		key := cfg.APIKey()
		if key == "" {
			return nil, fmt.Errorf("anthropic requires api_key_env to point at a non-empty env var")
		}
		return llmpkg.NewAnthropic(llmpkg.AnthropicConfig{
			Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ListOllamaModels probes the Ollama server for installed models. Surfaced to
// the settings UI so the user can pick from a dropdown rather than typing the
// model name. Best-effort; failures return an empty slice rather than an
// error string in the modal.
func (a *App) ListOllamaModels(endpoint string) []string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	resp, err := http.Get(endpoint + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := jsonDecodeStream(resp.Body, &body); err != nil {
		return nil
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out
}
