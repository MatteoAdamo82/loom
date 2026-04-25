// loom-mcp is a Model Context Protocol stdio server that exposes a Loom
// knowledge base to MCP-aware clients (Claude Code, Claude Desktop, …).
//
// The server reuses the same TOML config and SQLite store as the loom CLI;
// configure it with --config or LOOM_CONFIG.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/MatteoAdamo82/loom/internal/ingest"
	"github.com/MatteoAdamo82/loom/internal/lint"
	llmpkg "github.com/MatteoAdamo82/loom/internal/llm"
	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/MatteoAdamo82/loom/internal/storage"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	configPath := os.Getenv("LOOM_CONFIG")
	for i, a := range os.Args[1:] {
		if a == "--config" && i+1 < len(os.Args[1:]) {
			configPath = os.Args[i+2]
		} else if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}
	if configPath == "" {
		configPath = config.DefaultPath()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	if err := os.MkdirAll(dirOf(cfg.Storage.DBPath), 0o755); err != nil {
		fatal("ensure db dir: %v", err)
	}
	store, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		fatal("open store: %v", err)
	}
	defer store.Close()

	llmClient, err := buildLLM(cfg.LLM)
	if err != nil {
		fatal("init llm: %v", err)
	}

	srv := server.NewMCPServer("loom", Version, server.WithToolCapabilities(false))
	registerTools(srv, store, llmClient, cfg)

	if err := server.ServeStdio(srv); err != nil {
		fatal("serve stdio: %v", err)
	}
}

func registerTools(srv *server.MCPServer, store *storage.Store, client llmpkg.Client, cfg *config.Config) {
	srv.AddTool(
		mcp.NewTool("loom.ingest",
			mcp.WithDescription("Ingest a local file (txt, md, pdf, html) into Loom. Returns the source id, chunks count, and any notes/entities created."),
			mcp.WithString("path", mcp.Required(),
				mcp.Description("Absolute path to the file to ingest")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, err := req.RequireString("path")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			p := ingest.NewPipeline(store, client)
			p.ChunkCfg = ingest.ChunkConfig{
				MaxTokens: cfg.Ingest.ChunkTokens,
				Overlap:   cfg.Ingest.ChunkOverlap,
			}
			p.MaxAnalyze = cfg.Ingest.MaxAnalyze

			res, err := p.Ingest(ctx, path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			payload := map[string]any{
				"source_id":       res.Source.ID,
				"title":           res.Source.Title,
				"deduplicated":    res.Deduplicated,
				"chunks_created":  res.ChunksCreated,
				"notes_created":   noteSlugs(res.NotesCreated),
				"entities_linked": res.EntitiesLinked,
			}
			return jsonResult(payload), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom.query",
			mcp.WithDescription("Ask a natural-language question. Loom expands the query, runs hybrid BM25+graph retrieval, reranks with the LLM, and returns a synthesized answer with citations."),
			mcp.WithString("question", mcp.Required(),
				mcp.Description("The user question, in any language")),
			mcp.WithNumber("top_k",
				mcp.Description("Max number of candidates the synthesizer sees (default: 8)"),
				mcp.Min(1), mcp.Max(50),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			question, err := req.RequireString("question")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			eng := query.NewEngine(store, client)
			eng.Cfg = query.Config{
				BM25TopK:       cfg.Query.BM25TopK,
				GraphExpandHop: cfg.Query.GraphExpandHop,
				RerankTopK:     cfg.Query.RerankTopK,
			}
			if v := req.GetFloat("top_k", 0); v > 0 {
				eng.Cfg.RerankTopK = int(v)
			}

			ans, err := eng.Run(ctx, question)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			payload := map[string]any{
				"answer":     ans.Content,
				"citations":  ans.Citations,
				"expanded":   ans.Expanded,
				"candidates": ans.Candidates,
			}
			return jsonResult(payload), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom.search",
			mcp.WithDescription("Run a raw BM25 search against the FTS index without LLM expansion or rerank. Useful when you want a deterministic list of hits."),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("Free-text search terms")),
			mcp.WithNumber("limit",
				mcp.Description("Maximum hits to return (default: 10)"),
				mcp.Min(1), mcp.Max(100),
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
			hits, err := store.Search(ctx, sanitizeFTSTerms(q), limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(map[string]any{"hits": hits}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom.get_note",
			mcp.WithDescription("Fetch a single note by its slug, including content and basic link counts."),
			mcp.WithString("slug", mcp.Required(),
				mcp.Description("The note slug (e.g. 'andrej-karpathy')")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			slug, err := req.RequireString("slug")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			n, err := store.GetNoteBySlug(ctx, slug)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			inbound, _ := store.LinksToNote(ctx, n.ID)
			outbound, _ := store.LinksFromNote(ctx, n.ID)
			return jsonResult(map[string]any{
				"id":           n.ID,
				"slug":         n.Slug,
				"title":        n.Title,
				"kind":         n.Kind,
				"summary":      n.Summary,
				"keywords":     n.Keywords,
				"content":      n.Content,
				"version":      n.Version,
				"links_in":     len(inbound),
				"links_out":    len(outbound),
				"updated_at":   n.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom.list_notes",
			mcp.WithDescription("List notes, optionally filtered by kind."),
			mcp.WithString("kind",
				mcp.Description("Filter by kind: entity, concept, summary, synthesis, log")),
			mcp.WithNumber("limit",
				mcp.Description("Max rows (default: 50)"),
				mcp.Min(1), mcp.Max(500),
			),
			mcp.WithNumber("offset",
				mcp.Description("Skip rows (default: 0)"),
				mcp.Min(0),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind := req.GetString("kind", "")
			limit := int(req.GetFloat("limit", 50))
			if limit <= 0 {
				limit = 50
			}
			offset := int(req.GetFloat("offset", 0))
			notes, err := store.ListNotes(ctx, kind, limit, offset)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rows := make([]map[string]any, 0, len(notes))
			for _, n := range notes {
				rows = append(rows, map[string]any{
					"slug":     n.Slug,
					"title":    n.Title,
					"kind":     n.Kind,
					"summary":  n.Summary,
					"keywords": n.Keywords,
					"version":  n.Version,
				})
			}
			return jsonResult(map[string]any{"notes": rows, "count": len(rows)}), nil
		},
	)

	srv.AddTool(
		mcp.NewTool("loom.lint",
			mcp.WithDescription("Run hygiene checks: orphan notes, near-duplicates by keyword overlap, sources without notes."),
			mcp.WithNumber("min_overlap",
				mcp.Description("Jaccard threshold for duplicate detection (0..1, default 0.6)"),
				mcp.Min(0), mcp.Max(1),
			),
			mcp.WithNumber("min_keywords",
				mcp.Description("Minimum keywords on both sides to compare for duplicates (default 3, filters stub entities)"),
				mcp.Min(1), mcp.Max(20),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			min := req.GetFloat("min_overlap", 0)
			minKW := int(req.GetFloat("min_keywords", 0))
			report, err := lint.Run(ctx, store, lint.Config{
				MinKeywordOverlap: min,
				MinKeywords:       minKW,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			lint.SortFindings(report.Findings)
			return jsonResult(map[string]any{
				"stats":    report.Stats,
				"findings": report.Findings,
			}), nil
		},
	)
}

// jsonResult wraps a payload as a JSON-encoded text tool result.
func jsonResult(payload any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encode result: %v", err))
	}
	return mcp.NewToolResultText(string(b))
}

func noteSlugs(notes []*storage.Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Slug)
	}
	return out
}

// sanitizeFTSTerms is a duplicate of internal/query.sanitizeFTSQuery kept here
// to avoid coupling the MCP server to query internals; both should eventually
// move into a shared search helper.
func sanitizeFTSTerms(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '\'' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r > 127)
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		out = append(out, `"`+strings.ReplaceAll(f, `"`, ``)+`"`)
	}
	return strings.Join(out, " OR ")
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

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

func fatal(format string, a ...any) {
	fmt.Fprintln(os.Stderr, "loom-mcp:", fmt.Sprintf(format, a...))
	os.Exit(1)
}
