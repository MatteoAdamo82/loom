# Loom

> LLM memory that scales — without embeddings.

Loom is a single-binary knowledge base for LLM agents. It combines the ease of
use of [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
with the speed and precision of traditional RAG, using only **SQLite + FTS5
(BM25) + an LLM** — no vector databases, no embedding models, no ML libraries.

## Design in one paragraph

Where a RAG stack would compute vector embeddings, Loom asks the LLM to do the
semantic work: expand queries, rerank BM25 results, and generate concept
fingerprints at ingest time. All state lives in a single SQLite file. The LLM
layer is pluggable — Ollama locally, OpenAI / Anthropic via API.

## Status

**Pre-alpha.** MVP phase: storage layer + schema.

## Repository layout

```
cmd/loom        CLI entry point
cmd/loom-mcp    MCP stdio server (phase 2)
cmd/loom-gui    Desktop GUI (Wails, phase 3)
internal/storage  SQLite schema + repository
internal/ingest   Extract → chunk → analyze pipeline
internal/query    BM25 retrieval + LLM expand/rerank
internal/llm      Ollama / OpenAI / Anthropic adapters
internal/extract  Format-specific text extractors
pkg/loom          Public library API
```

## Build

```bash
go test ./...
go build ./cmd/loom
```

Requires Go 1.23+.

## License

MIT (see [LICENSE](LICENSE)).
