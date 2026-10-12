# Nexus — Architecture

> Status: **draft v0.1** — living document. Decisions are recorded as ADRs in [`decisions/`](decisions/).

Nexus is a personal **agent operating system**: one hub that knows your machines, your agents and your
knowledge, and lets you drive all of it from an iPhone app and a web dashboard.

- **Agents** do the work. An agent is a configuration (a Markdown note) bound to a **runtime**:
  Claude Code, Codex, a local model, local or cloud models, Anthrex, shell, browser…
- **Nodes** are your machines. A small Go daemon on each one executes agents and tools.
- **The Hub** routes, schedules, approves, remembers and notifies.
- **The Vault** is the memory: an Obsidian-compatible folder of Markdown notes, indexed into a graph.
- **Clients** (iOS, web, CLI) are thin views over one shared API and one event stream.

---

## 1. System overview

```mermaid
flowchart LR
  subgraph Clients
    IOS[iOS app<br/>SwiftUI]
    WEB[Web dashboard<br/>React + Tailwind]
    CLI[nexus CLI]
    EXT[Webhooks · Email · Siri<br/>Shortcuts · NFC]
  end

  subgraph Hub["Nexus Hub (nexusd, Go)"]
    API[Connect-RPC API]
    CMD[Command & Router]
    TASK[Task engine]
    WF[Workflow engine<br/>+ triggers]
    POL[Policy & approvals]
    BUS[(Event log)]
    VLT[Vault service<br/>indexer · search · graph]
    MEM[Memory pipeline]
    NOTIF[Notifier<br/>APNs · Web Push]
    MCPH[MCP server<br/>memory + nexus tools]
    DB[(SQLite)]
  end

  subgraph Nodes["Nexus Nodes (nexus-node, Go)"]
    N1[Mac · Claude Code · Codex · browser]
    N2[VPS · shell · watchdogs]
    N3[GPU box · Ollama]
    N4[Anthrex host]
  end

  GW[Cloud model APIs<br/>optional]
  LOCAL[Local models<br/>Ollama / llama.cpp]
  GIT[(Vault git remote)]
  OBS[Obsidian]

  IOS & WEB & CLI --> API
  EXT --> WF
  API --> CMD --> TASK
  WF --> TASK
  TASK <--> POL
  TASK --> BUS
  POL --> NOTIF --> IOS
  BUS --> API
  TASK <-->|NodeLink stream<br/>nodes dial out| Nodes
  VLT <--> GIT <--> OBS
  MEM --> VLT
  Nodes --> MCPH
  CMD --> LOCAL
  Nodes --> GW & LOCAL
  MEM --> LOCAL & GW
```

### Design principles

1. **Nexus is the brain; clients only talk to Nexus.** The iOS app, the web dashboard and the CLI
   have exactly one counterpart: the Hub API. They never connect to Nodes, models or integrations
   directly — files on a machine, agent runs, memory and status all go through Hub services
   (end-to-end sealed payloads included). The only other inbound channel to the phone is APNs,
   sent by the Hub. The Hub owns state, policy and memory; Nodes are stateless executors that can be
   added or wiped at any time.
2. **Files are the source of truth for knowledge.** The vault is plain Markdown in git; every index
   is disposable and rebuildable. You can always open it in Obsidian.
3. **Configuration is knowledge.** Agents, workflows and policies are vault notes too — editable
   from Obsidian, the web dashboard, the phone, or by an agent.
4. **Everything is an event.** Every state change is appended to an ordered event log. Clients sync
   by cursor, so offline phones catch up and the UI can replay any run.
5. **Nodes dial out.** No inbound ports on your machines. The Hub is the only thing exposed.
6. **Cheap first, escalate when needed.** Local models handle simple work; cloud models and coding
   agents are used when the task needs them.
7. **Human in the loop by policy, not by habit.** Each action class has a trust level; agents earn
   autonomy over time.
8. **Single binaries.** `nexusd` and `nexus-node` are self-contained Go binaries with embedded
   storage. No Kubernetes, no external database needed to start.

---

## 2. Components

### 2.1 Hub — `nexusd`

| Module | Responsibility |
|---|---|
| **API** | Connect-RPC services (gRPC, gRPC-Web and JSON over HTTP from one handler). Auth middleware. |
| **Command & Router** | Takes any free-form input (command bar, Siri, share sheet) and decides: search, capture, task, or workflow — and which agent/runtime tier. See [AGENTS.md §4](AGENTS.md#4-router). |
| **Task engine** | Task state machine, assignment to Nodes, retries, escalation, sub-task trees, cancellation. |
| **Workflow engine** | Executes workflow notes (steps, conditions, loops, parallel branches, approvals). |
| **Triggers** | Cron schedules, webhooks, email, file/vault changes, location & NFC events from iOS, Node alerts. |
| **Policy & approvals** | Evaluates every sensitive action against trust levels, budgets and per-agent rules; creates approval requests; verifies biometric signatures. |
| **Event log** | Append-only, monotonically sequenced log (SQLite). Fan-out to subscribers. Source for replay, audit and sync. |
| **Vault service** | Owns the canonical vault git repo: read/write notes, auto-commit, push/pull, watch for external edits. |
| **Indexer** | Parses notes → nodes/edges graph, FTS5 full-text, embeddings (sqlite-vec). Rebuildable. |
| **Memory pipeline** | Async workers: summarize episodes, extract entities/facts/preferences, link notes, propose fact updates. |
| **Notifier** | APNs (token auth) for iOS, Web Push for the dashboard; notification categories with inline actions. |
| **MCP server** | Exposes vault/memory and Nexus abilities (`ask_user`, `request_approval`, `spawn_agent`…) to any MCP-capable runtime. |
| **Integrations** | Connectors: email (IMAP/Gmail), calendar, GitHub, Home Assistant, Telegram, webhooks. |

### 2.2 Node — `nexus-node`

| Module | Responsibility |
|---|---|
| **NodeLink** | One long-lived bidirectional stream to the Hub (HTTP/2, mTLS). Heartbeats, assignments, events, reconnect with resume. |
| **Runtime adapters** | Start/stream/steer/stop agents: `claude-code`, `codex`, `openai` (any OpenAI/Anthropic-compatible API), `ollama`, `anthrex`, `shell`, `browser`. |
| **Tool host** | Native tools: shell, filesystem, browser (Playwright), clipboard, screenshots, HTTP; plus external MCP servers configured on that machine. |
| **Workspaces** | Each run gets a working directory (optionally a git worktree / container) with resource limits. |
| **Local secrets** | API keys, cookies, browser profiles stay on the Node. The Hub only stores references. |
| **Telemetry** | CPU, memory, disk, battery, online state, installed runtimes and versions. |
| **Vault sync (optional)** | Keep a local clone of the vault (e.g. for Obsidian on the Mac). |

### 2.3 Clients

| Client | Stack | Role |
|---|---|---|
| **iOS** | SwiftUI, Swift Concurrency, `connect-swift`, SwiftData offline cache, App Intents, WidgetKit, ActivityKit | Primary remote control. See [CLIENTS.md](CLIENTS.md). |
| **Web** | Vite, React, TypeScript, Tailwind, `@connectrpc/connect-web`, TanStack Query/Router | Dashboard, Agent Studio, Workflow Builder, vault editor and graph. |
| **CLI** | Go, same generated client | Scripting, ops, debugging; `nexus run "..."`. |

### 2.4 LLM access

- **Model providers** are configured in the `llm` module: local models (Ollama) and/or cloud APIs,
  OpenAI- or Anthropic-compatible. Agents use *aliases* (`fast-local`, `smart`, `best`, `embed`)
  from `policies/models.md`, so swapping models is a config change. See [MODELS.md](MODELS.md).
- **Local models** (Ollama / llama.cpp, OpenAI-compatible endpoint) on any Node for routing,
  classification, tagging, summaries and extraction.
- **Claude Code** and **Codex** are *runtimes*, not model endpoints — they run as processes on Nodes
  and bring their own agent loops.

---

## 3. Core domain model

```mermaid
erDiagram
  DEVICE ||--o{ APPROVAL : decides
  NODE ||--o{ RUN : executes
  AGENT ||--o{ TASK : "assigned to"
  WORKFLOW ||--o{ TASK : spawns
  TASK ||--o{ TASK : "sub-task of"
  TASK ||--o{ RUN : "attempts"
  RUN ||--o{ EVENT : emits
  RUN ||--o{ APPROVAL : requests
  TASK }o--o{ NOTE : "reads / writes"
  NOTE }o--o{ NOTE : links
```

| Entity | Lives in | Notes |
|---|---|---|
| **Agent** | vault `agents/*.md` | Runtime, model tier, tools, node selector, limits, policy overrides, playbook. |
| **Workflow** | vault `workflows/*.md` | Triggers + steps. |
| **Policy** | vault `policies/*.md` | Trust levels, budgets, global rules. |
| **Note / Entity** | vault | People, projects, places, products, episodes… |
| **Task** | Hub DB | A unit of intent ("buy these groceries"). Has a tree of sub-tasks. |
| **Run** | Hub DB | One attempt of a task on a node + runtime. Retries/escalations create new runs. |
| **Event** | Hub DB (event log) | Every message, tool call, output chunk, state change, cost record. |
| **Approval** | Hub DB | Pending decision with action class, risk, payload preview, expiry. |
| **Device** | Hub DB | Paired phone/browser/CLI with its public key and permissions. |
| **Node** | Hub DB | Registered machine with tags, capabilities, status. |

### Task lifecycle

```mermaid
stateDiagram-v2
  [*] --> queued
  queued --> routing: no agent chosen
  routing --> needs_input: low confidence → ask user
  needs_input --> routing
  routing --> assigned
  queued --> assigned: agent given
  assigned --> running: node accepted
  running --> waiting_approval: policy requires approval
  waiting_approval --> running: approved
  waiting_approval --> failed: denied / expired
  running --> waiting_input: agent asks a question
  waiting_input --> running: user replied
  running --> escalating: low confidence / check failed
  escalating --> assigned: next runtime tier
  running --> succeeded
  running --> failed
  queued --> cancelled
  running --> cancelled
  succeeded --> [*]
  failed --> [*]
  cancelled --> [*]
```

---

## 4. Key flows

### 4.1 Command from the phone

```mermaid
sequenceDiagram
  autonumber
  participant P as iPhone
  participant H as Hub
  participant R as Router (local model)
  participant N as Node (Mac)
  participant A as Runtime (Claude Code)

  P->>H: Command("fix the failing tests in nexus")
  H->>R: classify(input, agent catalog, context)
  R-->>H: agent=code-fixer, tier=heavy, conf=0.93
  H->>N: Assign(run, agent spec, workspace)
  N->>A: claude -p --output-format stream-json …
  loop streaming
    A-->>N: messages / tool calls
    N-->>H: RunEvent
    H-->>P: Event stream (live view / Live Activity)
  end
  A->>N: permission request: git push
  N->>H: ApprovalRequest(code_push)
  H-->>P: Push notification [Approve] [Deny]
  P->>H: Decide(approve)
  H->>N: Resolve(approval)
  N->>A: allow
  A-->>N: done
  N-->>H: RunCompleted(summary, cost)
  H->>H: write episode note to vault, update memory
  H-->>P: Result card in Inbox
```

### 4.2 Grocery run (browser agent + memory + approvals)

1. Share a list (text / photo / voice) to Nexus → Command.
2. Router → `grocery-continente` agent (rule match: shopping list).
3. Hub expands the list from memory: `[[Milk]]` → *Mimosa lactose-free 1L ×6* (preference note).
4. Node with tag `browser` runs the agent; browser profile with the logged-in session stays on that Node.
5. Out-of-stock or substitution above threshold → `ask_user` → push with options.
6. Cart ready → approval class `spend` with total, item list and screenshot → **Face ID** approval.
7. Checkout with the payment method already saved in the account (agents never handle card data).
8. Order saved as an episode note, linked to products; preferences updated.

### 4.3 Capture to vault

Share sheet / voice / scan → `Capture` → raw episode note in `inbox/` immediately (works offline,
synced later) → memory pipeline (local model) summarizes, tags, extracts entities, proposes links →
note moved/linked; low-confidence facts go to the **Facts inbox** for confirmation.

### 4.4 Event sync

Clients call `EventService.Subscribe(since_seq)`. The Hub replays events after `since_seq`, then
streams live. Clients persist the last seq. This one mechanism gives: live UIs, offline catch-up,
multi-device consistency, run replay and audit.

---

## 5. Protocols

- **Schema:** Protobuf in [`proto/nexus/v1`](../proto/nexus/v1), managed with `buf`.
  Code generated for Go (`connect-go`), TypeScript (`connect-es`) and Swift (`connect-swift`).
- **Client ↔ Hub:** Connect protocol over HTTPS (HTTP/2 where available). Server streaming for events.
  Works from browsers without a proxy.
- **Node ↔ Hub:** `NodeLinkService.Connect` bidirectional stream over HTTP/2 with mTLS. The Node
  dials out; resume tokens make reconnects lossless.
- **Agents ↔ Nexus tools:** MCP. Every Node runs a local MCP endpoint that proxies to the Hub, so
  Claude Code, Codex and any MCP-aware runtime get `memory.search`, `vault.write`, `ask_user`,
  `request_approval`, `spawn_agent`, `notify` for free.
- **LLMs:** OpenAI-compatible chat completions (Unify Gateway, Ollama).
- **Push:** APNs HTTP/2 with token (`.p8`) auth; Web Push (VAPID) for the dashboard.

---

## 6. Storage

| Data | Store | Why |
|---|---|---|
| Operational state (tasks, runs, events, approvals, devices, nodes, costs) | SQLite (WAL) on the Hub | Embedded, fast, one file, trivial backups. Behind a `store` interface so Postgres is possible later. |
| Knowledge, agents, workflows, policies | Vault: Markdown files in git | Human-readable, Obsidian-compatible, versioned, syncable. |
| Vault index (graph, FTS, vectors) | Separate SQLite (`index.db`) with FTS5 + sqlite-vec | Disposable; rebuilt from files. |
| Large blobs (screenshots, PDFs, audio) | `vault/_attachments/` (git LFS optional) or content-addressed blob dir | Linked from notes. |
| Secrets | Node-local keychain / encrypted file; Hub stores references only | Secrets never transit to clients. |

SQLite driver: pure-Go / no-cgo preferred so binaries cross-compile cleanly — see
[ADR-0005](decisions/0005-sqlite-embedded-storage.md).

---

## 7. Security model (summary)

Full details in [SECURITY.md](SECURITY.md). The connection is designed so that **no single
compromised component — network, APNs, or even the Hub — can take control of a machine**.

1. **Private network:** WireGuard/Tailscale; the Hub has no public port in the recommended mode.
2. **mTLS 1.3** with a pinned private CA and hybrid post-quantum key exchange.
3. **Signed requests** (Secure Enclave keys, nonces, timestamps, counters) — no replay.
4. **End-to-end sealed envelopes** (HPKE + signatures) between phone and Node; the Hub only routes.
5. **Edge verification:** Nodes trust the **owner root key** (in the iPhone's Secure Enclave), not
   the Hub, and verify Face ID-signed approvals bound to the exact action hash before executing.
6. **Least privilege:** scoped run tokens, tool allow-lists, sandboxes, secrets stay on Nodes.
7. **Tamper-evident audit** (hash-chained, signed, mirrored) and instant alerts.

Pairing is in person (QR + App Attest + emoji safety code); encrypted notification payloads keep
content away from APNs; kill switch and remote revoke are owner-signed so Nodes obey them directly.
See [ADR-0008](decisions/0008-zero-trust-hub-edge-verification.md).

---

## 8. Repository layout

```
nexus/
├── proto/nexus/v1/          # API schema — source of truth for every client (buf)
├── backend/                 # Go module github.com/danielpina1/nexus/backend
│   ├── kernel/              # plug-in SDK: Module, Host, tool/runtime/trigger registries, service interfaces
│   │   └── kerneltest/      # fake Host for module tests
│   ├── gen/                 # generated Go + connect-go code
│   ├── cmd/
│   │   ├── nexusd/          # Hub binary (modules.go = which hub modules are compiled in)
│   │   ├── nexus-node/      # Node binary (modules.go = which runtimes/tools are compiled in)
│   │   └── nexus/           # CLI
│   ├── internal/
│   │   ├── host/            # module loader: config, dependency order, lifecycle
│   │   ├── hubserver/       # HTTP/h2c server, CORS, auth middleware, static web
│   │   ├── store/ events/   # SQLite store, ordered event log + fan-out
│   │   ├── tasks/ approvals/ policy/ router/ command/   # orchestration brain
│   │   ├── security/        # PKI, envelopes, signed approvals, owner trust, audit, pairing
│   │   ├── vault/           # Markdown vault, index, search, graph, agent catalog
│   │   ├── nodelink/        # hub side of the node link (Dispatcher)
│   │   ├── node/            # node daemon (run manager, local tools)
│   │   ├── llm/             # OpenAI-compatible client (Unify Gateway, Ollama)
│   │   └── runtime/         # echo, shell, agentloop, openai, claudecode, codex …
│   └── configs/             # example configs
├── web/                     # Vite + React + Tailwind dashboard (generated TS in src/gen)
├── ios/                     # SwiftUI app, widgets, extensions (XcodeGen) + NexusKit package
├── vault-template/          # starter vault: agents, workflows, policies
└── docs/
```

### Modularity

Every capability is a **module** (`kernel.Module`) registered in `init()` and enabled by config.
Modules communicate only through the `kernel.Host`:

- **Registries** — tools (`ToolRegistry`), runtimes (`RuntimeRegistry`), trigger sources
  (`TriggerRegistry`). Adding a tool/runtime/trigger never touches the core.
- **Services** — well-known names (`events`, `tasks`, `dispatcher`, `router`, `policy`, `vault`,
  `llm`, `auth`…) resolved by interface. Swapping an implementation = enabling a different module
  that provides the same service.
- **Event bus** — every module can publish and subscribe.

A new integration (Home Assistant, Gmail, a new agent runtime, Anthrex) is one package with an
`init()` and one blank import in `cmd/nexusd/modules.go` or `cmd/nexus-node/modules.go`.

---

## 9. Deployment topologies

| Topology | When |
|---|---|
| **Fortress:** Hub on a home machine behind the **UniFi Gateway**; the phone uses the UniFi WireGuard VPN / Teleport ([NETWORK.md](NETWORK.md)) | Recommended. Nothing public. |
| **Blind relay:** Hub at home, a VPS only relays sealed traffic and sends APNs | When Tailscale on the phone is not wanted. |
| **Cloud Hub:** Hub on a VPS behind strict mTLS | Simplest to reach; Nodes still verify everything (edge verification). |
| **Hub + Node in one process** (`nexusd --with-node`) | Development and single-machine setups. |

---

## 10. Related documents

- [FEATURES.md](FEATURES.md) — full feature catalogue (iOS, web, platform)
- [CLIENTS.md](CLIENTS.md) — iOS app and web dashboard information architecture
- [IOS_LIVE.md](IOS_LIVE.md) — Live Activities, Dynamic Island, notifications, widgets
- [AGENTS.md](AGENTS.md) — agent definitions, runtimes, router, escalation
- [VAULT.md](VAULT.md) — knowledge vault format, graph, memory pipeline
- [WORKFLOWS.md](WORKFLOWS.md) — workflow format and catalogue
- [NETWORK.md](NETWORK.md) — reaching the Hub through the UniFi Gateway (VPN, DNS, firewall) of workflows
- [SECURITY.md](SECURITY.md) — pairing, keys, trust levels, threat model
- [ROADMAP.md](ROADMAP.md) — phased delivery plan
- [OPEN_QUESTIONS.md](OPEN_QUESTIONS.md) — decisions still needed
- [decisions/](decisions/) — architecture decision records
