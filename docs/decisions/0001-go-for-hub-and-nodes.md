# ADR-0001: Go for the Hub and Nodes

- Status: accepted
- Date: 2026-10-11

## Context
Nexus needs a long-running hub and a daemon on many machines (macOS, Linux, maybe Windows), with lots
of concurrent streams (runs, events, node links), low memory use and easy deployment.

## Decision
Write `nexusd`, `nexus-node` and the `nexus` CLI in Go, in one module.

## Consequences
- Single static binaries, trivial cross-compilation (`GOOS/GOARCH`), small footprint on nodes.
- Goroutines/channels fit streaming and fan-out naturally.
- Strong ecosystem for gRPC/Connect, SQLite, git, fsnotify, Playwright (`playwright-go`), MCP.
- ML-heavy work (embeddings, fine-tuning) is delegated to model servers (Ollama, llama.cpp,
  cloud APIs), not done in Go.
