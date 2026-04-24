// Package cli wires Loom's command-line surface. Commands live in their own
// files (init, ingest, query, notes, config) and share the persistent --config
// flag through context on the cobra command.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/storage"
	"github.com/spf13/cobra"
)

type runtime struct {
	Cfg   *config.Config
	Store *storage.Store
}

// Version is stamped at build time via -ldflags "-X ...Version=...".
var Version = "dev"

func Root() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "loom",
		Short:         "LLM memory: SQLite + BM25 + LLM, no embeddings.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", config.DefaultPath(),
		"path to the Loom config TOML")

	root.AddCommand(
		cmdInit(&configPath),
		cmdIngest(&configPath),
		cmdQuery(&configPath),
		cmdNotes(&configPath),
		cmdNoteShow(&configPath),
		cmdLint(&configPath),
		cmdConfigShow(&configPath),
	)
	return root
}

// bootstrap loads the config, opens the store, and returns a runtime that
// commands can use. Callers MUST defer rt.Store.Close().
func bootstrap(configPath string) (*runtime, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if err := ensureDir(cfg.Storage.DBPath); err != nil {
		return nil, err
	}
	store, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, err
	}
	return &runtime{Cfg: cfg, Store: store}, nil
}

func ensureDir(dbPath string) error {
	dir := dirOf(dbPath)
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
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

// cliContext is a shorthand that returns the command's context falling back
// to context.Background().
func cliContext(cmd *cobra.Command) context.Context {
	if cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}
