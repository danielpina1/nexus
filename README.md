# Nexus

**A personal agent operating system.** One hub that knows your machines, your agents and your
knowledge — driven from an iPhone app and a web dashboard.

- 🤖 **Agents for everything** — Claude Code, Codex, local models, any model through Unify Gateway,
  Anthrex, shell and browser agents. An agent is just a Markdown note.
- 🖥️ **Your machines as Nodes** — a small Go daemon on each machine runs agents and tools; nodes
  dial out, so nothing at home is exposed.
- 🧠 **A knowledge vault** — Obsidian-compatible Markdown in git, indexed into a graph with hybrid
  search. Every task leaves a note; agents learn from them.
- 📱 **iPhone first** — chat with agents, watch runs live, approve with Face ID from the lock screen,
  share anything to Nexus, Siri and Shortcuts, widgets and Live Activities.
- 🛡️ **Human in the loop by policy** — trust levels per action, budgets, audit log, kill switch.

> Status: **architecture phase**. Nothing runs yet — see the roadmap.

## Documentation

| Doc | |
|---|---|
| [Architecture](docs/ARCHITECTURE.md) | Components, data model, flows, protocols, storage |
| [Features](docs/FEATURES.md) | Full feature catalogue with priorities |
| [Clients](docs/CLIENTS.md) | iOS app and web dashboard |
| [Agents](docs/AGENTS.md) | Agent definitions, runtimes, router, escalation, trust |
| [Vault](docs/VAULT.md) | Knowledge vault, graph, memory pipeline |
| [Workflows](docs/WORKFLOWS.md) | Workflow format and catalogue |
| [Security](docs/SECURITY.md) | Pairing, keys, approvals, threat model |
| [Roadmap](docs/ROADMAP.md) | Phased plan |
| [Open questions](docs/OPEN_QUESTIONS.md) | Decisions still needed |
| [Decisions](docs/decisions/) | Architecture decision records |

API schema: [`proto/nexus/v1`](proto/nexus/v1) (`buf lint` clean).
Starter vault with example agents and workflows: [`vault-template/`](vault-template/).

## Stack

Go (hub, nodes, CLI) · Protobuf + Connect-RPC · SQLite (FTS5, sqlite-vec) · React + Vite + Tailwind
(web) · SwiftUI (iOS) · MCP for agent tools.
