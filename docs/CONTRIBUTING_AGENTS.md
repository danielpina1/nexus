# Shared brief for parallel development

This file coordinates parallel contributors (human or AI). Read it first.

## Repo
- `proto/nexus/v1` — API schema (do not edit without coordinating; regenerate with `make gen`).
- `backend/` — Go module `github.com/danielpina1/nexus/backend` (Go 1.24).
  - `backend/gen/` generated Go (connect-go **v1** API, `connectrpc.com/connect`). Never edit.
  - `backend/kernel/` the plug-in SDK: `Module`, `Host`, registries, well-known service interfaces
    (`services.go`). Read all of it before writing a module.
  - `backend/kernel/kerneltest` fake Host for unit tests.
  - `backend/internal/host` module loader (used by nexusd and nexus-node).
  - `backend/internal/sqlitex` SQLite open helper (modernc, pure Go; FTS5 available).
- `web/` React + Vite + Tailwind dashboard; generated TS in `web/src/gen` (protobuf-es v2).
- `ios/` SwiftUI app; generated Swift in `ios/NexusKit/Sources/NexusAPI/Generated`.
- `vault-template/` starter vault (agents, workflows, policies as Markdown).
- `docs/` architecture — the design you are implementing.

## Module conventions (backend)
- One package per module under `backend/internal/<area>`; register in `init()` with
  `kernel.RegisterModule(func() kernel.Module { return &Module{} })`.
- Names: hub modules `store`, `events`, `tasks`, `approvals`, `policy`, `router`, `command`,
  `security`, `vault`, `nodelink`, `llm`; node modules `node.llm`, `runtime.echo`, `runtime.shell`,
  `runtime.openai`, `runtime.claudecode`, `runtime.codex`.
- Provide services with the `kernel.Svc*` names; resolve other modules' services with
  `kernel.Resolve[T]`. Resolve **optional** services lazily (at Start or per call) and degrade
  gracefully when absent. Declare hard dependencies in `ModuleInfo.Requires`.
- Mount Connect handlers with `h.Handle(nexusv1connect.NewXxxServiceHandler(impl))`.
- Hub tools: `h.Tools().Register(kernel.ToolFunc{...})`, names like `memory.search`, `ask_user`.
- Persist with `kernel.Store` (`SvcStore`): each module creates its own tables via
  `Store.Migrate("<module>", []string{...})`.
- IDs: ULIDs with a prefix (`tsk_`, `run_`, `apr_`, `evt_` …) via `github.com/oklog/ulid/v2`.
- Logging: `log/slog` from `h.Logger()`.
- Tests: table-driven, `go test ./internal/<yours>/...` must pass. No network in tests.

## Rules while working in parallel
- Only create/edit files in the directories you own (listed in your task).
- Do **not** run `go mod tidy` (other packages are being written concurrently).
  `go get <pkg>@<version>` for a new dependency is OK — mention it in your report.
- Build/test only your packages (`go build ./internal/<yours>/... ./cmd/<yours>/...`);
  `./...` may be broken by others' work in progress.
- Do **not** commit or push; the integrator does that.
- Do not touch the sibling `anthrex` repository.

## Wire conventions
- Hub API listens on `:7700` (HTTP/1.1 + h2c), Connect protocol, all services under `/nexus.v1.*`.
- Auth: `Authorization: Bearer <token>`. Dev mode: `security.dev_token` in `nexus.yaml`.
  Nodes send `Authorization: Bearer <node token>` + `X-Nexus-Node-Id` until mTLS lands.
- Event stream: `EventService.Subscribe(since_seq)`; clients store the last `seq`.
- Inbox items are events whose body is `approval` (pending), `question`, or `notice`.
