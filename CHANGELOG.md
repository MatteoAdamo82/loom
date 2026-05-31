# Changelog

All notable changes to Loom are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.2] - 2026-05-31

### Fixed
- **OCR no longer exhausts memory.** Vision requests now pin `num_ctx`
  (16384) and `num_predict` (4096) instead of letting Ollama allocate the
  model's full default context. `glm-ocr` advertises a 131072-token context,
  which inflated the KV cache to ~10 GB of (V)RAM for a 1.1B model — on a
  machine with limited unified memory this swapped to a standstill and
  inference stalled mid-scan, so the index never finished and every run looked
  like a full re-scan. Capping the context drops the footprint to a few GB and
  lets scans complete.
- **`loom version` now reports the real version.** The goreleaser ldflag
  targeted a non-existent `cmd/loom/cli.Version`; the variable lives in
  `package main`, so released binaries always printed `dev`. Now stamped
  correctly via `-X main.Version`.

## [0.4.1] - 2026-05-31

### Fixed
- **`ocr_model` is now honored.** `config.merge()` copied every `[llm]` field
  except `ocr_model`, so the setting was parsed and then silently dropped: the
  `VisionExtractor` was never registered and image/PDF OCR did nothing even when
  configured. Image and scanned-PDF indexing now works as documented.

### Added
- **GLM-OCR via Ollama** — when `ocr_model` is set in the `[llm]` config block,
  Loom transcribes image files (PNG/JPG/WebP/GIF/TIFF) and scanned PDF pages
  through an Ollama vision model and indexes the extracted text like any other
  document.
- **`loom-http`** — an optional REST server (`cmd/loom-http`) that exposes the
  Loom index over HTTP for applications that aren't MCP-aware (e.g. a backend or
  microservice that wants to use Loom as a retrieval layer):
  - `GET /healthz` — liveness check.
  - `POST /search {query, limit?}` — raw BM25 hits (`rel_path, title, summary,
    content, rank`), **no LLM call**. Ideal for "retrieve here, answer in your
    own model".
  - `POST /scan {force?}` — (re)index the notes folder.
  It reuses the same `config.toml` as `loom` and `loom-mcp` (`--config` /
  `LOOM_CONFIG`); listen address via `LOOM_HTTP_ADDR` (default `:8080`).

### Notes
- The HTTP server is **additive and optional**. It does not change Loom's core
  idea — a folder of files on your machine as the source of truth, queried
  locally with no embeddings and no vector DB.

## [0.4.0]

### Added
- Support for multiple notes directories via `notes_dirs` (point Loom at folders
  that already exist on your machine; the legacy singular `notes_dir` is migrated
  automatically).

## [0.3.1] - [0.3.0]

- Earlier releases. See the Git history and the
  [releases page](https://github.com/MatteoAdamo82/loom/releases) for details.

[Unreleased]: https://github.com/MatteoAdamo82/loom/compare/v0.4.2...HEAD
[0.4.2]: https://github.com/MatteoAdamo82/loom/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/MatteoAdamo82/loom/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/MatteoAdamo82/loom/releases/tag/v0.4.0
