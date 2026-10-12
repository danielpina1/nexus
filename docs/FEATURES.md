# Nexus — Feature Catalogue

> Status: brainstorm v0.1. Priority: **P0** = MVP, **P1** = next, **P2** = later, **💡** = idea to explore.

## Core platform

| Feature | Priority |
|---|---|
| Hub (`nexusd`) single binary with embedded SQLite and web UI | P0 |
| Node (`nexus-node`) single binary, dials out, auto-reconnect | P0 |
| Pairing devices and nodes by QR code | P0 |
| Unified event log with cursor sync (live + offline catch-up) | P0 |
| Task engine with state machine, sub-tasks, retries, cancel | P0 |
| Agents defined as vault notes | P0 |
| Approvals with trust levels (auto / notify / approve / biometric) | P0 |
| APNs push notifications with actions | P0 |
| Kill switch (stop everything) | P0 |
| Budgets (LLM cost and real spend) per agent / day / run | P1 |
| Earned autonomy suggestions | P2 |
| Multi-hub / family members with separate vaults | 💡 |

## Runtimes

| Feature | Priority |
|---|---|
| Native agent loop over OpenAI/Anthropic-compatible APIs (Ollama, cloud) | P0 |
| Local models via Ollama / llama.cpp | P0 |
| Claude Code runtime (headless, streaming, approvals bridged) | P0 |
| Shell runtime with templated commands | P0 |
| Codex runtime | P1 |
| Browser runtime (Playwright, persistent profiles) | P1 |
| Anthrex adapter | P1 (needs interface) |
| Race / compare strategies across runtimes | P2 |
| Container sandbox per run | P2 |

## Router & intelligence

| Feature | Priority |
|---|---|
| Command bar → intent (search / capture / task / workflow) | P0 |
| Rule stage + local LLM classifier | P0 |
| Embedding similarity stage | P1 |
| Decision log + corrections dataset | P1 |
| Escalation across tiers | P1 |
| Fine-tuned local router | P2 |
| Planner agents with task trees and bundled approvals | P1 |
| Proactive suggestions from patterns | P2 |

## Vault & memory

| Feature | Priority |
|---|---|
| Git-backed Markdown vault, Obsidian-compatible | P0 |
| Indexer: links graph + FTS5 | P0 |
| Capture (text, link, file, photo) to `inbox/` | P0 |
| Episode note written for every task | P0 |
| Embeddings + hybrid search with graph expansion | P1 |
| Memory pipeline: summarize, extract entities/facts, auto-link | P1 |
| Facts inbox (confirm / correct) | P1 |
| Temporal facts (`valid_from` / `valid_to`) | P2 |
| MCP server for memory | P0 |
| Vault sync to desktops (Obsidian) | P1 |
| Client-side encryption | P2 |

## iOS

| Feature | Priority |
|---|---|
| Pairing, Home, Chat, Tasks, Machines tabs | P0 |
| Live run view with streaming | P0 |
| Interactive notifications (approve / deny / reply) | P0 |
| Face ID signed approvals | P0 |
| Share extension | P0 |
| Vault tab (search, read, capture) | P1 |
| App Intents / Siri / Shortcuts | P1 |
| Live Activities + Dynamic Island | P1 |
| Widgets (home + lock screen) | P1 |
| Voice mode | P1 |
| Document scanner | P1 |
| Control Center controls / Action Button | P2 |
| Apple Watch | P2 |
| Location and NFC triggers | P2 |
| Phone-as-a-node tools (calendar, contacts, Shortcuts) | P2 |
| Focus filters, Spotlight, Handoff | P2 |
| CarPlay voice-only mode | 💡 |

## Web dashboard

| Feature | Priority |
|---|---|
| Overview, Inbox, Chat, Tasks & Runs, Machines | P0 |
| ⌘K command bar | P0 |
| Agent Studio (form + Markdown, test sandbox) | P1 |
| Vault editor + graph view | P1 |
| Costs | P1 |
| Workflow Builder (visual) | P2 |
| Router analytics | P2 |
| Web terminal and file manager | P1 |
| Security & audit | P1 |

## Workflows & triggers

| Feature | Priority |
|---|---|
| Cron schedules | P0 |
| Workflow notes with steps, conditions, approvals | P1 |
| Webhooks | P1 |
| Email trigger | P1 |
| Node events (watchdogs) | P1 |
| Location / NFC / calendar triggers | P2 |
| Template library | P1 |
| Dry run | P1 |

## Integrations

| Integration | Priority |
|---|---|
| UniFi Gateway access (WireGuard / Teleport VPN) | P0 |
| GitHub | P1 |
| Email (IMAP / Gmail) | P1 |
| Calendar (via iOS EventKit and/or CalDAV/Google) | P1 |
| Continente (browser agent) | P1 |
| Home Assistant | P2 |
| Telegram bot as backup channel | P2 |
| Any MCP server (attach to agents) | P1 |
| Finance imports (bank CSV) | P2 |

## Ideas parking lot 💡

- **"Nexus, continue"** — resume whatever you were last doing on any device with one tap.
- **Agent marketplace** — share agent/workflow notes as gists; import with one click.
- **Time-travel** — "show me the vault as it was on 1 March" (git history + temporal facts).
- **Shadow mode** — a new agent runs in dry-run alongside you for a week before it gets real permissions.
- **Personal API** — expose selected workflows as authenticated webhooks for other apps.
- **Context packs** — "work mode" bundles agents, notes and policies; switch with Focus.
- **Screen understanding** — Node screenshot + vision model to drive desktop apps without APIs.
- **Family sharing** — shared grocery list and household agents with separate private vaults.
- **Cost-aware scheduling** — run heavy local jobs at night on the GPU box.
- **Daily voice briefing** — the morning brief as audio (TTS) for the commute.
- **Ambient capture** — highlight text anywhere on the Mac → hotkey → vault.
