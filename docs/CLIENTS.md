# Clients — iOS App and Web Dashboard

> Status: draft v0.1

Both clients are views over the same API and event stream. **They talk only to the Nexus Hub** —
never directly to Nodes, models or third-party services; the Hub is the single brain and gateway. The rule: **anything you can do on the
web you can at least see and approve on the phone; anything quick you can do on the phone without
opening the web.**

## 1. Shared concepts

- **Command bar** — one input for everything (text, voice, attachments). Sent to `CommandService`;
  the router decides what it is. Shows a preview ("→ Grocery agent on mac-mini") before running when
  confidence is low.
- **Inbox** — the single list of things that need you, ranked:
  1. Biometric / approval requests (with expiry)
  2. Agent questions (`ask_user`)
  3. Failures and alerts
  4. Results ready
  5. Facts to confirm
  6. Suggestions
- **Cards** — every inbox item is a typed card with inline actions, rendered the same way in iOS,
  web, notifications and widgets.
- **Live run view** — streaming transcript of a run: messages, tool calls (collapsible), artifacts
  (screenshots, diffs, files), cost meter, and controls: *Reply, Pause, Try harder, Cancel*.

## 2. iOS app

### Tabs

The tab bar matches the web's phone tab bar (Home, Inbox, Chat, Work, More), and every screen is
built from the shared design system (`design/MASTER.md`; tokens generated into
`ios/DesignSystem/Generated` by `make ios-tokens`, components in `ios/DesignSystem/Kit`).

| Tab | Content |
|---|---|
| **Home** | Greeting, metrics (Running, Needs you, Spent today, Machines), brief card, the top of the inbox ("Needs you"), running tasks as live cards, recent results, machines with live CPU / RAM / disk, favourite agents as buttons. |
| **Inbox** | Approvals (Face ID class first), questions and notices ranked by urgency, with All / Approvals / Questions / Notices filters. Approve and Deny sit inside each card; "All clear" when empty. |
| **Chat** | "Nexus" thread (auto-routed) + per-agent threads. Voice mode (push-to-talk and hands-free). Attach photos, files, scans. |
| **Work** | Section tabs like the web: **Tasks** (Running / Scheduled / Done, search, task tree, retry, escalate), **Teams** (team list, team detail with "Run team", live team run: member tree, timeline, blackboard, limit meters, inline approvals, team messages), **Agents**, **Architect** (draft creation, review, sandbox tests, refine, publish via approval) and **Spend**. |
| **More** | **Knowledge** (search, backlinks, quick capture), **Machines** (nodes with live stats, running agents, file browser, wake-on-LAN, kill switch), **Settings**, the connection indicator and the Light / System / Dark control. |

Settings: Hub connection & pairing, appearance (Light / System / Dark, OLED black in dark),
devices, notifications, trust levels, budgets, integrations, model aliases (read-only).

### System integrations

| iOS feature | Use in Nexus |
|---|---|
| **Share extension** | Send anything to: auto, a specific agent, the vault, or a machine. Remembers last target per content type. |
| **App Intents / Shortcuts / Siri** | "Ask Nexus…", "Run workflow…", "Capture…", "Status of…"; every agent and workflow becomes a Shortcuts action; parameters supported. |
| **Interactive notifications** | Approve / Deny / Reply / choice buttons; Face ID for biometric class; notification grouping per task. |
| **Live Activities + Dynamic Island** | Progress of long runs (step, %, cost), with Approve inline when waiting. |
| **Widgets** | Inbox count, machine status, brief, favorite workflows (interactive buttons), last result. |
| **Control Center controls / Action Button** | Quick capture, voice command, a chosen workflow, kill switch. |
| **Lock Screen widgets** | Pending approvals, running tasks. |
| **Apple Watch app** | Approvals, voice command, complications for status. |
| **VisionKit document scanner + Live Text** | Receipts, letters, handwritten lists → capture or agent. |
| **Speech / audio recording** | Voice notes and meeting recordings → transcription on Node (local Whisper) or on-device. |
| **Location (geofences)** | Triggers: arrive home, near supermarket, at office. |
| **Core NFC** | Tags as workflow triggers. |
| **EventKit / Contacts** | Agents can read/write calendar, reminders, contacts — executed on-device on request from the Hub. |
| **Spotlight (Core Spotlight)** | Index vault notes and agents for system search. |
| **Focus filters** | Show only work or personal agents/inbox per Focus. |
| **Handoff / Universal Links** | Continue a chat or open a run on the web and vice versa. |
| **Background refresh + push** | Keep inbox and cache fresh; silent pushes for sync. |
| **HealthKit (optional)** | Sleep/steps in the brief. |

### "Phone as a node"

The iPhone itself can expose a few tools to agents (with per-request permission): current location,
calendar/reminders/contacts, photos picker, clipboard, Shortcuts execution. This lets an agent do
"add this to my calendar" or "run my Shortcut X" on the phone.

### Offline behaviour

- SwiftData cache of inbox, recent tasks, pinned notes, agents.
- Commands and captures queued with idempotency keys; sent when online.
- Event stream resumes from the last stored sequence number.

### Tech

SwiftUI, Swift Concurrency, `connect-swift` generated clients, SwiftData, Keychain + Secure Enclave
keys, App Groups shared between app, share extension, widgets and intents.

## 3. Web dashboard

| Section | Content |
|---|---|
| **Overview** | Live wall of running agents, machines health, inbox, today's cost (cloud vs local), recent results, quick command bar. |
| **Inbox** | Same as iOS, plus bulk actions. |
| **Chat** | A conversation with the assistant (source `chat`, one `conversation_id` per chat; no router, see AGENTS.md §2.4). Replies stream into the bubble with Markdown and a caret, labelled with agent · engine · exact model id; tool calls are compact chips, questions and approvals appear inline. Chat tasks (params `chat: true`) send no result notice and no result push: the reply is in the chat (approvals and questions still notify). A reply whose `spawn_agent` sub-tasks still run shows "Waiting for N sub-tasks…" (phase `waiting_children`) until the agent assembles the final answer. Side pane: the full live run. |
| **Tasks & Runs** | Search/filter by agent, node, status, time, cost. Full trace: prompts, tool calls, outputs, approvals, events. Replay, retry, escalate, compare runs side by side. |
| **Agent Studio** | Create/edit agent notes with a form + raw Markdown; test chat in a sandbox (dry-run); version history with diffs; usage stats per agent. |
| **Workflow Builder** | Visual node editor (trigger → steps → branches → approvals) ↔ workflow note; dry-run; run history. |
| **Vault** | Markdown editor with live preview, wikilink autocomplete, backlinks, full graph view (filter by type/tag/time), timeline, Facts inbox, "what agents changed" feed with git diffs. |
| **Machines** | Per-node metrics, runtimes installed (Claude Code, Codex, Ollama, browsers), logs, web terminal, file manager, workspaces, update node. |
| **Router** | Decision log, corrections, accuracy charts, dataset export, deploy fine-tuned router. |
| **Costs** | Spend per agent/model/runtime/day, local vs cloud ratio, budgets and alerts. |
| **Integrations** | Connect email, calendar, GitHub, Home Assistant, Telegram, MCP servers, webhooks. |
| **Security** | Devices, pairing QR, permissions, trust levels, budgets, audit log, kill switch. |
| **Settings** | Hub config, model aliases, notification rules. |

Global: ⌘K command bar, keyboard shortcuts, dark/light theme, real-time updates via the event stream.

### Tech

Vite + React + TypeScript + Tailwind CSS, `@connectrpc/connect-web` + `connect-query`
(TanStack Query), TanStack Router, a graph renderer (WebGL force graph) for the vault, CodeMirror 6
for Markdown, React Flow for the workflow builder. Served by `nexusd` as embedded static files
(single binary).
