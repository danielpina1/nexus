# Nexus

**A personal agent operating system.** One hub that knows your machines, your agents and your
knowledge — driven from an iPhone app and a web dashboard.

- 🤖 **Agents for everything** — Claude Code, Codex, local models, any OpenAI- or Anthropic-compatible model (local Ollama or cloud),
  Anthrex, shell and browser agents. An agent is just a Markdown note.
- 🖥️ **Your machines as Nodes** — a small Go daemon on each machine runs agents and tools; nodes
  dial out, so nothing at home is exposed.
- 🧠 **A knowledge vault** — Obsidian-compatible Markdown in git, indexed into a graph with hybrid
  search. Every task leaves a note; agents learn from them.
- 📱 **iPhone first** — chat with agents, watch runs live, approve with Face ID from the lock screen,
  share anything to Nexus, Siri and Shortcuts, widgets and Live Activities.
- 🏠 **Home-first** — the Hub runs at home behind your UniFi Gateway; the iPhone reaches it over the
  UniFi VPN (WireGuard / Teleport), nothing is exposed to the internet.
- 🛡️ **Zero-trust security** — private network, mTLS, end-to-end encrypted phone ↔ machine
  envelopes; machines only obey Face ID-signed approvals from your iPhone, even if the Hub is
  compromised. Trust levels, budgets, audit log, kill switch.

> Status: **Phase 0–1 foundation running.** Hub, nodes, runtimes (echo, shell, Claude Code, Codex,
> OpenAI-compatible/Ollama), tasks, approvals, vault, security core and the web dashboard work end to
> end. The iOS app is written but not yet compiled (needs Xcode). See the roadmap for what's next.

## Quickstart

```bash
# 1. Build
cd backend && go build -o bin/ ./cmd/... && cd ..
cd web && npm ci && npm run build && cd ..

# 2. Create a hub (writes nexus.yaml with a dev token + a vault from vault-template)
mkdir -p ~/nexus-hub && cd ~/nexus-hub
~/path/to/nexus/backend/bin/nexusd init -template ~/path/to/nexus/vault-template
#   set server.web_dir to <repo>/web/dist and add a node token under security.node_tokens:
#     node_tokens: ["my-laptop:<random secret>"]
~/path/to/nexus/backend/bin/nexusd serve

# 3. Connect a machine (copy backend/cmd/nexus-node/node.example.yaml → node.yaml, set id/token)
nexus-node run -config node.yaml

# 4. Use it
open http://localhost:7700                       # web dashboard (paste the dev token in Settings)
export NEXUS_TOKEN=<dev token>
nexus run "hello ?approve" -agent echo -follow   # streams, then waits for approval
nexus approve <approval-id>                      # or approve from the dashboard / iPhone
```

Try the dashboard without a hub: `cd web && npm run dev:mock`.

## Layout

| Directory | What |
|---|---|
| `backend/` | Go: `nexusd` (hub), `nexus-node` (machines), `nexus` (CLI), kernel SDK + modules |
| `web/` | React + Tailwind dashboard |
| `ios/` | SwiftUI app, widgets, Live Activities, extensions (XcodeGen) |
| `proto/` | API schema for every client |
| `vault-template/` | Starter vault: agents, workflows, policies |
| `docs/` | Architecture and design |

## Documentation

| Doc | |
|---|---|
| [Architecture](docs/ARCHITECTURE.md) | Components, data model, flows, protocols, storage |
| [Features](docs/FEATURES.md) | Full feature catalogue with priorities |
| [Clients](docs/CLIENTS.md) | iOS app and web dashboard |
| [iOS live surfaces](docs/IOS_LIVE.md) | Live Activities, Dynamic Island, notifications, widgets |
| [Agents](docs/AGENTS.md) | Agent definitions, runtimes, router, escalation, trust |
| [Vault](docs/VAULT.md) | Knowledge vault, graph, memory pipeline |
| [Workflows](docs/WORKFLOWS.md) | Workflow format and catalogue |
| [Security](docs/SECURITY.md) | Pairing, keys, approvals, threat model |
| [Network](docs/NETWORK.md) | Reaching the Hub through the UniFi Gateway (VPN, DNS, firewall) |
| [Roadmap](docs/ROADMAP.md) | Phased plan |
| [Open questions](docs/OPEN_QUESTIONS.md) | Decisions still needed |
| [Decisions](docs/decisions/) | Architecture decision records |

API schema: [`proto/nexus/v1`](proto/nexus/v1) (`buf lint` clean).
Starter vault with example agents and workflows: [`vault-template/`](vault-template/).

## Stack

Go (hub, nodes, CLI) · Protobuf + Connect-RPC · SQLite (FTS5, sqlite-vec) · React + Vite + Tailwind
(web) · SwiftUI (iOS) · MCP for agent tools.
