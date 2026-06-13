// Loom CLI: drop files in a folder, scan to index them, ask questions.
//
//	loom init    — create ~/.loom/config.toml and ~/loom/
//	loom scan    — re-index the notes folder
//	loom ask "…" — query the index
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/extract"
	"github.com/MatteoAdamo82/loom/internal/index"
	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loom:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Allow `--config FOO` either before or after the subcommand.
	pre, args := pullLeadingConfig(args)

	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}
	cmd, rest := args[0], args[1:]
	if pre != "" {
		rest = append([]string{"--config", pre}, rest...)
	}
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("loom", Version)
		return nil
	case "init":
		return cmdInit(rest)
	case "scan":
		return cmdScan(rest)
	case "ask":
		return cmdAsk(rest)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func pullLeadingConfig(args []string) (string, []string) {
	if len(args) >= 2 && args[0] == "--config" {
		return args[1], args[2:]
	}
	if len(args) >= 1 && strings.HasPrefix(args[0], "--config=") {
		return strings.TrimPrefix(args[0], "--config="), args[1:]
	}
	return "", args
}

func usage() {
	fmt.Fprint(os.Stderr, `loom — a folder of files + an LLM as your knowledge base

usage:
  loom init                 create ~/.loom/config.toml and the notes folder
  loom scan [--force]       (re)index the notes folder
  loom ask "your question"  query the index

flags:
  --config PATH             use an alternate config file (default ~/.loom/config.toml)
`)
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func cmdInit(args []string) error {
	cfgPath, _ := flagConfig(args)

	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("config already exists at %s — leaving as-is.\n", cfgPath)
	} else {
		cfg := config.Default()
		if err := config.Save(cfg, cfgPath); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", cfgPath)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for _, d := range cfg.NotesDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create notes dir: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	if len(cfg.NotesDirs) == 1 {
		fmt.Printf("notes folder: %s\n", cfg.NotesDirs[0])
	} else {
		fmt.Println("notes folders:")
		for _, d := range cfg.NotesDirs {
			fmt.Printf("  - %s\n", d)
		}
	}
	fmt.Printf("index db:     %s\n", cfg.DBPath)
	fmt.Println("\ndrop your files (md, pdf, html, txt) into the notes folder, then run `loom scan`.")
	return nil
}

// ---------------------------------------------------------------------------
// scan
// ---------------------------------------------------------------------------

func cmdScan(args []string) error {
	cfgPath, rest := flagConfig(args)
	force := false
	for _, a := range rest {
		if a == "--force" {
			force = true
		}
	}

	rt, err := bootstrap(cfgPath)
	if err != nil {
		return err
	}
	defer rt.store.Close()

	ix := index.New(rt.cfg.NotesDirs, rt.store, rt.llm)
	ix.Embedder = rt.embedder
	if m := rt.cfg.LLM.OCRModel; m != "" {
		ix.Registry = extract.NewRegistryWithOCR(rt.cfg.LLM.Endpoint, m)
	}
	ix.OnEvent = func(e index.Event) {
		switch e.Phase {
		case "summarize":
			if e.Err != nil {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s — error: %v\n", e.Index, e.Total, e.RelPath, e.Err)
				return
			}
			fmt.Printf("  [%d/%d] %s\n", e.Index, e.Total, e.RelPath)
		case "delete":
			fmt.Printf("  removed %s (no longer on disk)\n", e.RelPath)
		}
	}

	ctx := signalContext()
	var res *index.Result
	if force {
		res, err = ix.Force(ctx)
	} else {
		res, err = ix.Scan(ctx)
	}
	if err != nil {
		return err
	}

	fmt.Printf("\nscan done in %s — %d added, %d updated, %d moved, %d skipped, %d removed",
		res.Duration.Round(0), res.Added, res.Updated, res.Moved, res.Skipped, res.Removed)
	if res.Failed > 0 {
		fmt.Printf(", %d failed", res.Failed)
		fmt.Println()
		for _, fe := range res.Errors {
			fmt.Fprintf(os.Stderr, "  - %s: %v\n", fe.RelPath, fe.Err)
		}
	} else {
		fmt.Println()
	}
	return nil
}

// ---------------------------------------------------------------------------
// ask
// ---------------------------------------------------------------------------

func cmdAsk(args []string) error {
	cfgPath, rest := flagConfig(args)
	if len(rest) == 0 {
		return fmt.Errorf(`usage: loom ask "your question"`)
	}
	question := strings.Join(rest, " ")

	rt, err := bootstrap(cfgPath)
	if err != nil {
		return err
	}
	defer rt.store.Close()

	ctx := signalContext()
	res, err := query.Stream(ctx, rt.store, rt.llm, rt.embedder, question, query.DefaultTopK, func(chunk string) {
		fmt.Print(chunk)
	})
	if err != nil {
		return err
	}
	fmt.Println()
	if len(res.Citations) > 0 {
		fmt.Println()
		fmt.Println("citations:")
		for _, c := range res.Citations {
			fmt.Printf("  - %s\n", c.RelPath)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// runtime helpers
// ---------------------------------------------------------------------------

type runtime struct {
	cfg      *config.Config
	store    *storage.Store
	llm      llm.Client
	embedder llm.Embedder // nil → BM25 only
}

func bootstrap(cfgPath string) (*runtime, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, err
	}
	for _, d := range cfg.NotesDirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	lc, err := makeLLM(cfg.LLM)
	if err != nil {
		store.Close()
		return nil, err
	}
	var emb llm.Embedder
	if cfg.Embeddings.Enabled {
		e, eerr := llm.BuildEmbedder(cfg.Embeddings.Provider, cfg.Embeddings.Model, cfg.Embeddings.Endpoint, cfg.Embeddings.APIKey())
		if eerr != nil {
			store.Close()
			return nil, fmt.Errorf("init embeddings: %w", eerr)
		}
		emb = e
	}
	return &runtime{cfg: cfg, store: store, llm: lc, embedder: emb}, nil
}

func makeLLM(cfg config.LLMConfig) (llm.Client, error) {
	switch cfg.Provider {
	case "ollama", "":
		return llm.NewOllama(llm.OllamaConfig{
			Endpoint: cfg.Endpoint,
			Model:    cfg.Model,
		}), nil
	case "openai":
		key := cfg.APIKey()
		if key == "" {
			return nil, fmt.Errorf("openai provider requires api_key_env to point at a non-empty env var")
		}
		return llm.NewOpenAI(llm.OpenAIConfig{
			Endpoint: cfg.Endpoint,
			Model:    cfg.Model,
			APIKey:   key,
		}), nil
	case "anthropic":
		key := cfg.APIKey()
		if key == "" {
			return nil, fmt.Errorf("anthropic provider requires api_key_env to point at a non-empty env var")
		}
		return llm.NewAnthropic(llm.AnthropicConfig{
			Endpoint: cfg.Endpoint,
			Model:    cfg.Model,
			APIKey:   key,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q (supported: ollama, openai, anthropic)", cfg.Provider)
	}
}

// flagConfig pulls a leading "--config PATH" out of args. Anything else is
// passed through to the caller.
func flagConfig(args []string) (string, []string) {
	cfg := config.DefaultPath()
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				cfg = args[i+1]
				i++
			}
		default:
			out = append(out, args[i])
		}
	}
	return cfg, out
}

func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx
}
