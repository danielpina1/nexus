# Workflows

> Status: draft v0.1

A **workflow** is a vault note in `workflows/` describing a trigger and a set of steps. Simple
workflows are just "run this agent on a schedule"; complex ones chain agents, conditions, loops,
parallel branches and approvals. The web dashboard has a visual builder that reads and writes the
same note.

## 1. Format

```markdown
---
type: workflow
id: morning-brief
name: Morning brief
enabled: true
triggers:
  - cron: "30 7 * * 1-5"
    tz: Europe/Lisbon
  - intent: ["morning brief", "what's my day"]
inputs: {}
steps:
  - id: cal
    agent: calendar
    task: "List today's events and conflicts"
  - id: mail
    agent: inbox-triage
    task: "Summarize important unread email since yesterday 18:00"
  - id: ops
    tool: nexus.status            # nodes, failed runs, pending approvals
  - id: weather
    tool: http.get
    with: { url: "https://..." }
  - id: brief
    agent: writer
    model: fast-local
    task: |
      Write a short morning brief from: {{ steps.cal.output }}, {{ steps.mail.output }},
      {{ steps.ops.output }}, {{ steps.weather.output }}
  - id: save
    tool: vault.daily_append
    with: { text: "{{ steps.brief.output }}" }
  - id: send
    tool: notify
    with: { title: "Good morning", body: "{{ steps.brief.output }}", card: brief }
---

Notes about this workflow, history, ideas…
```

### Step kinds

| Kind | Example |
|---|---|
| `agent` | Run an agent with a task prompt; output is text + structured result. |
| `team` | Start a team run (`team: <id>`, `prompt`/`task`, `inputs`), wait for it; output = the team's result. |
| `tool` | Call a tool directly (no LLM). |
| `if` | `if: "steps.cart.output.total > 120"` (CEL expressions). |
| `foreach` | Iterate over a list, optional `parallel: 4`. |
| `parallel` | Run branches concurrently, join. |
| `approve` | Explicit approval card with a preview. |
| `ask` | Ask the user a question with choices. |
| `wait` | Wait for time, event or condition. |
| `workflow` | Call another workflow. |

### Team steps

```yaml
  - id: squad
    team: research-squad              # a team id (teams/<id>.md); "team:<id>" also accepted
    prompt: "Full report: {{ inputs.question }}"   # or task:
    inputs: { question: "{{ inputs.question }}" }   # rendered, passed as the run's params
    timeout: 50m                       # cancels the team run when exceeded
```

The step creates the root task of a team run (`CreateTaskRequest.team_id`, source `workflow`), waits
until the run ends and fails when the run fails (`on_error: continue` goes on). `steps.<id>.output`
is the team's final answer (the orchestrator's, or the hub strategy's when the team has none);
`steps.<id>.result` is its structured result plus `task_id`, `team_id` and `cost_minor_units`. The
team's own limits apply; `model:` is not allowed (set models on the team's agents). Dry runs do
not start the team. Example: `vault-template/workflows/squad-report.md`.

### Triggers

| Trigger | Source |
|---|---|
| `cron` | Hub scheduler |
| `intent` | Router matches a command |
| `share` | iOS share sheet by content type / domain |
| `webhook` | `POST /hooks/<id>` with secret |
| `email` | IMAP / Gmail filter |
| `vault` | Note created/changed matching a query |
| `location` | iOS geofence enter/exit |
| `nfc` | iOS NFC tag id |
| `node` | Node event (disk > 90%, offline, process died) |
| `calendar` | N minutes before an event |
| `price` / `watch` | A watcher detected a change |

## 2. Catalogue

Workflows planned as templates in `vault-template/workflows/`. ⭐ = MVP candidates.

### Daily life
| Workflow | Trigger | What it does |
|---|---|---|
| ⭐ **Groceries** | share list / "buy the usual" | Expand list from preferences → fill Continente cart → ask on substitutions → biometric checkout approval → save order episode. |
| **Usual list suggestion** | weekly cron | Predict next list from order history; one tap to start Groceries. |
| ⭐ **Morning brief** | 07:30 weekdays | Calendar, important mail, weather, overnight agent results, pending approvals. |
| **Evening shutdown** | geofence: arrive home | What got done, what's open tomorrow, Home Assistant scene. |
| **Inbox triage** | new email | Classify, archive noise, draft replies for important threads → approval. |
| **Receipts & bills** | share/scan receipt, email invoice | OCR → amount/store/category → `finance/` → monthly summary. |
| **Price watch** | "tell me when X < €Y" | Daily check; push when hit. |
| **Travel planner** | intent | Options note → pick on phone → draft bookings for approval → calendar + packing list. |
| **Appointments** | intent | Find slots in calendar, draft the message/booking, remind the day before. |
| **Leaving home** | geofence exit / NFC | Lights off, heating down, check doors (Home Assistant). |

### Knowledge
| Workflow | Trigger | What it does |
|---|---|---|
| ⭐ **Capture** | share sheet / voice / scan | Raw note → summary, tags, entities, links → facts review queue. |
| ⭐ **Ask my brain** | question in command bar | Hybrid search → answer with citations. |
| **Meeting notes** | audio recording | Transcript → summary → action items → tasks for agents. |
| **Research report** | intent | Planner fans out to research agents → report note linked to existing notes. |
| **Read later digest** | weekly | Summaries of unread `resources/`. |
| **Weekly review** | Sunday cron | What was captured, done, open loops, suggestions. |
| **Contact enrichment** | new person note | Pull context from email/calendar history. |

### Code & machines
| Workflow | Trigger | What it does |
|---|---|---|
| ⭐ **Code task** | intent / GitHub issue label | Claude Code or Codex in a worktree → live stream → approve commit/PR. |
| **Compare attempts** | intent "try both" | Claude Code and Codex in parallel → diff view → pick one. |
| **CI fixer** | GitHub check failed | Reproduce, fix, propose PR for approval. |
| ⭐ **Server watchdog** | node event / cron | Health checks → safe auto-fix (restart service) → report; destructive fixes need approval. |
| **Backup check** | daily | Verify backups exist and restore-test a sample. |
| **Remote files** | intent "send me…" | Find file on a node → share to phone. |
| **Dependency updates** | weekly | Update, run tests, open PRs for approval. |

### Work / Anthrex
| Workflow | Trigger | What it does |
|---|---|---|
| **Anthrex job** | intent / share | Hand a task to Anthrex via its adapter, store results in the vault. |
| **Client proposal** | intent | Gather notes about the client → draft → review. |
| **Recurring report** | cron | Collect data → draft → approval → send. |

### Composite example — "Dinner for 6 on Saturday"
`planner` → `recipes` (menu from preferences and dietary notes) → `groceries` (buy ingredients)
→ `calendar` (block time, invite) → one bundled approval on the phone.

## 3. Execution semantics

- Each workflow run is a **root task**; each step is a sub-task (visible in the task tree).
- Step outputs are stored as events; reruns can resume from a failed step.
- Idempotency keys on external actions (orders, emails) prevent double execution on retry.
- `dry_run: true` executes everything except external/spend actions and shows what would happen.
