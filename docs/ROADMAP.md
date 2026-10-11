# Roadmap

> Status: draft v0.1. Each phase ends with something usable from the iPhone.

## Phase 0 — Foundations
- [ ] Protobuf API v1 (`proto/nexus/v1`) + `buf` codegen for Go, TS, Swift
- [ ] Go module layout, CI (lint, test, build), release binaries
- [ ] `nexusd`: SQLite store + migrations, event log, Connect handlers skeleton
- [ ] `nexus-node`: NodeLink stream, heartbeat, telemetry
- [ ] Security core: private CA + mTLS 1.3, owner root key, device/node enrolment with safety
      codes, signed requests, sealed envelopes, edge-verified approvals, hash-chained audit log
- [ ] Pairing (devices + nodes) with App Attest
- [ ] Tailscale-based "Fortress" deployment guide

**Demo:** pair a node and see it online from `nexus` CLI.

## Phase 1 — Talk to agents from the phone (MVP)
- [ ] Task engine + state machine
- [ ] Runtimes: `openai` (Unify Gateway), `ollama`, `shell`, `claude-code`
- [ ] Agents loaded from vault `agents/*.md`
- [ ] Router v1: rules + local LLM classifier
- [ ] Approvals (approve + biometric) and APNs
- [ ] Web: Overview, Inbox, Chat with live run view, Tasks, Machines
- [ ] iOS: pairing, Home, Chat, Tasks, Machines, Face ID approvals, Share extension
- [ ] iOS live: notifications (approve/deny/reply, thread per task, collapse), Run Live Activity
      with Hub updates, encrypted notification payloads
- [ ] Episode note per task

**Demo:** from the iPhone, ask Claude Code on the Mac to fix a bug, watch it live, approve the push.

## Phase 2 — Memory
- [ ] Vault service (git), indexer (graph + FTS5), embeddings (sqlite-vec)
- [ ] `memory.search` hybrid retrieval, MCP server
- [ ] Capture pipeline + Facts inbox
- [ ] Web vault editor + graph view; iOS Vault tab
- [ ] Desktop vault sync for Obsidian

**Demo:** share an article from the phone, ask a question about it a week later and get a cited answer.

## Phase 3 — More runtimes and smarter routing
- [ ] Codex, browser (Playwright), Anthrex adapter
- [ ] Escalation tiers, decision log, corrections
- [ ] Planner agents, task trees, bundled approvals
- [ ] Budgets and Costs page

**Demo:** Groceries on Continente end to end with biometric checkout.

## Phase 4 — iOS power features
- [ ] App Intents / Siri / Shortcuts
- [ ] Push-to-start, Pulse activity, Follow from web, StandBy
- [ ] Rich notifications (attachments, custom content UI), agents as contacts
- [ ] Widgets, Control Center controls, Action Button, Watch
- [ ] Voice mode, document scanner
- [ ] Offline queue and cache hardening

## Phase 5 — Automation
- [ ] Workflow engine (steps, CEL conditions, approvals), triggers (cron, webhook, email, node)
- [ ] Templates: morning brief, inbox triage, watchdog, weekly review, receipts
- [ ] Workflow Builder (web)
- [ ] Agent Studio with sandbox

## Phase 6 — Integrations
- [ ] GitHub, email, calendar, Home Assistant, Telegram
- [ ] Location / NFC / calendar triggers, phone-as-a-node tools

## Phase 7 — Intelligence
- [ ] Fine-tuned local router
- [ ] Earned autonomy
- [ ] Proactive suggestions and pattern detection
- [ ] Temporal facts and time-travel views
