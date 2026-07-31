// loom-mcp is a Model Context Protocol stdio server that exposes a Loom
// knowledge base to MCP-aware clients (Claude Code, Claude Desktop, …).
//
// It reuses the same TOML config as the loom CLI; configure it with --config
// or LOOM_CONFIG.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/extract"
	"github.com/MatteoAdamo82/loom/internal/index"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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

	client, err := buildLLM(cfg.LLM)
	if err != nil {
		fatal("init llm: %v", err)
	}

	// Optional embeddings → hybrid search. Non-fatal if unavailable.
	var embedder llmpkg.Embedder
	if cfg.Embeddings.Enabled {
		if e, eerr := llmpkg.BuildEmbedder(cfg.Embeddings.Provider, cfg.Embeddings.Model, cfg.Embeddings.Endpoint, cfg.Embeddings.APIKey()); eerr != nil {
			log.Printf("loom-mcp: embeddings enabled but unavailable, using BM25 only: %v", eerr)
		} else {
			embedder = e
		}
	}

	srv := server.NewMCPServer("loom", Version, server.WithToolCapabilities(false))
	registerTools(srv, store, client, embedder, cfg)

	if err := server.ServeStdio(srv); err != nil {
		fatal("serve stdio: %v", err)
	}
}

func registerTools(srv *server.MCPServer, store *storage.Store, client llmpkg.Client, embedder llmpkg.Embedder, cfg *config.Config) {
	srv.AddTool(
		mcp.NewTool("loom_ask",
			mcp.WithDescription("Ask a natural-language question against the indexed notes folder. Loom runs a single LLM call over the BM25 top hits and returns a grounded answer with file citations."),
			mcp.WithString("question", mcp.Required(),
				mcp.Description("The user question, in any language")),
			mcp.WithNumber("top_k",
				mcp.Description("How many files the LLM gets to read (default: 5)"),
				mcp.Min(1), mcp.Max(20),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("question")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			topK := int(req.GetFloat("top_k", float64(query.DefaultTopK)))
			res, err := query.Answer(ctx, store, client, embedder, q, topK)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"answer":    res.Answer,
				"citations": res.Citations,
				"used":      res.Used,
			}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom_search",
			mcp.WithDescription("Search the index for relevant files without generating an answer. BM25 keyword search by default, or hybrid (BM25 + semantic vector) when embeddings are enabled. Use when you want a ranked list of relevant files."),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("Free-text search terms")),
			mcp.WithNumber("limit",
				mcp.Description("Maximum hits to return (default: 10)"),
				mcp.Min(1), mcp.Max(50),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := int(req.GetFloat("limit", 10))
			if limit <= 0 {
				limit = 10
			}
			hits, err := query.Retrieve(ctx, store, embedder, q, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rows := make([]map[string]any, 0, len(hits))
			for _, h := range hits {
				rows = append(rows, map[string]any{
					"rel_path": h.RelPath,
					"title":    h.Title,
					"kind":     h.Kind,
					"summary":  h.Summary,
					"keywords": h.Keywords,
					"rank":     h.Rank,
				})
			}
			return jsonResult(map[string]any{"hits": rows}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom_scan",
			mcp.WithDescription("(Re)index the notes folder. Walks the configured notes_dir, asks the LLM to summarise new/modified files, and updates the SQLite index. Returns counts."),
			mcp.WithBoolean("force",
				mcp.Description("Re-summarise every file, even unchanged ones (default: false)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			force := req.GetBool("force", false)
			ix := index.New(cfg.NotesDirs, store, client)
				ix.Embedder = embedder
				if m := cfg.LLM.OCRModel; m != "" {
					ix.Registry = extract.NewRegistryWithOCR(cfg.LLM.Endpoint, m)
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
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(res.Payload()), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom_list_files",
			mcp.WithDescription("List every indexed file with its title, kind, summary, and keywords. Cheap — no LLM call."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			files, err := store.List(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rows := make([]map[string]any, 0, len(files))
			for _, f := range files {
				rows = append(rows, map[string]any{
					"rel_path":   f.RelPath,
					"title":      f.Title,
					"kind":       f.Kind,
					"summary":    f.Summary,
					"keywords":   f.Keywords,
					"indexed_at": f.IndexedAt.Format("2006-01-02T15:04:05Z07:00"),
				})
			}
			return jsonResult(map[string]any{"files": rows, "count": len(rows)}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom_get_file",
			mcp.WithDescription("Fetch the full extracted content of a single file (so the caller can quote it verbatim or feed it to a different prompt)."),
			mcp.WithString("rel_path", mcp.Required(),
				mcp.Description("File path relative to notes_dir, e.g. 'ricette.md' or 'papers/llm-wiki.pdf'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			rel, err := req.RequireString("rel_path")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			f, err := store.GetByRelPath(ctx, rel)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"rel_path": f.RelPath,
				"title":    f.Title,
				"kind":     f.Kind,
				"summary":  f.Summary,
				"keywords": f.Keywords,
				"content":  f.Content,
			}), nil
		},
	)
}

func jsonResult(payload any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode result: %v", err))
	}
	return mcp.NewToolResultText(string(b))
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
	fmt.Fprintln(os.Stderr, "loom-mcp:", fmt.Sprintf(format, a...))
	os.Exit(1)
}
