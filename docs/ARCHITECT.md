# Agent Architect

> Status: implemented (backend/internal/architect, proto/nexus/v1/architect.proto,
> vault-template/agents/agent-architect.md, vault-template/workflows/agent-tuning.md).

The Agent Architect turns a request ("tell me when the AirPods drop below €150") into a complete,
validated agent note — plus a workflow note when the request implies a schedule or trigger, or a
team note for multi-specialist work — tests it in a sandbox and publishes it **only after the owner
approves**.

```
request ─► Draft ─► validate + auto-fix ─► READY ─► TestRun (sandbox) ─► Publish ─► approval ─► agents/<id>.md
              ▲            │ problems → INVALID          │                     (owner)       workflows/<id>.md
              └── Refine (feedback) ◄─────────────────────┘                                    teams/<id>.md
```

## Where requests come from

- **Command bar / Siri:** when the learning router finds no fitting agent (`params.no_fit`, see
  [ROUTING.md](ROUTING.md)) the choices end with **"Create a new agent for this"**. Picking it
  (a forced route whose `agent_id` is that text) calls `architect.DraftFromCommand` and answers with
  the draft id (`route.params.architect_draft_id`); a notice arrives when the draft is ready.
- **ArchitectService** (web Agent Studio / iOS, later): `Draft`, `Refine`, `TestRun`, `Publish`,
  `ListDrafts`, `GetDraft`, `Discard`.
- **Weekly agent review:** `workflows/agent-tuning.md` runs `architect.tune` on Sundays.

## Drafting

Two paths, chosen by `architect.mode` (`auto` default):

| Path | When | How |
|---|---|---|
| `agent` | default (no `architect.model`), e.g. Claude/ChatGPT subscriptions without API keys | Runs the **agent-architect** agent as an ordinary task (`runtime: auto`, tier heavy → Claude Code) with a prompt listing the live tools, node tools, existing agents and skills; waits for the terminal task and parses its JSON answer (`Task.result` or the summary). |
| `llm` | `architect.model` set (e.g. `best` mapped to a cloud model) | One `kernel.LLM` chat with the architect system prompt, JSON mode. |

The answer is one JSON object: `agent_markdown`, optional `workflow_markdown` / `team_markdown`,
`rationale`, `risks`, `test_cases` (`input` + `expect`). `Draft` returns immediately in state
DRAFTING (or waits with `wait: true`); `Refine(draft_id, feedback)` makes a new revision with the
previous notes, validation problems, failed tests and all feedback in the prompt.
`update_agent_id` drafts an improved version of an existing agent (its current note and diff are
included).

## Validation and auto-fix

Every draft (and `architect.validate`) is checked against the live system:

| Check | Action |
|---|---|
| frontmatter parses; `agentdef.Parse` has no errors (description, runtime, limits…) | problem |
| `type`, `runtime` (→ `auto`), `tier` (→ `medium`), `limits` (→ 20m / €0.50) missing or invalid | auto-fix |
| id invalid → slug; id already used (not an update) → `<id>-2` | auto-fix |
| tools not in the hub tool registry nor the known node tools (`browser`, `fs.read`, `shell.exec`, `node.exec`, `node.files`, `anthrex`) | removed |
| `architect.*` tools on any agent but agent-architect | removed |
| **`anthrex.*` tools on a non-coding agent** | **problem** (rejected) |
| policy looser than the defaults (e.g. `spend: auto`) | raised to the default |
| `spend: biometric`, `write_external: approve` (and `destructive: biometric` for destructive tools) | always set |
| `memory.writes` in `agents/`, `policies/`, `workflows/`, `shops/`, `teams/`, `skills/` | moved to `episodes/<id>/` |
| unknown skills (or no skills catalog) | removed |
| no `examples:` | noted (the router learns faster with 3–5) |
| empty body (instructions) | problem |
| workflow: `workflow.Parse` errors, unknown agents/tools in steps | problem (renamed agent ids are followed) |
| team: `TeamCatalog.UpsertTeam(validateOnly)` / `teamdef.Parse` errors | problem |

**Coding-only tools.** `anthrex.*` tools drive repositories and are reserved for **coding agents**:
an agent with `runtime: anthrex`, or `coding: true` in its `claude:` or `codex:` block (the same
rule the node enforces in `internal/node/codeonly.go`, which drops these tools from every other
run). The architect only gives anthrex tools to agents it designs as coding agents, marks those
`coding: true`, and validation rejects any other draft that lists them with: *"tool anthrex.… is
reserved for coding agents: give it only to an agent that runs on the anthrex runtime or sets
`coding: true` in its claude:/codex: block"*.

Each fix is recorded in `draft.fixes` with its explanation; remaining problems put the draft in
INVALID (refine it or fix and re-draft). Publishing re-validates (the catalog may have changed).

## Sandbox test runs

`TestRun` runs each test case and records output, tool calls, **blocked actions** and checks.
Expectations `contains:`, `not_contains:`, `calls:`, `no_calls:` and `json` are checked; free text
is listed as `? …` for the owner. Modes, best first:

1. `sandbox_task` (preferred; the tasks module implements `architect.SandboxTaskCreator` and
   `SandboxReporter`, `backend/internal/tasks/sandbox.go`) — the draft runs as a **real task** of an
   ephemeral, unpublished agent (`eph:sandbox-<id>`), on a real runtime, under a strict policy the hub
   re-applies at every assignment:
   - tools: only hub tools of class read/compute (the hub also refuses any other tool call of the
     run); node tools, `ask_user`, `spawn_agent`, `schedule`, `team.*` and coding-only tools are
     removed; never the `anthrex` runtime, CLI runtimes run non-coding (no file/shell tools);
   - policy: every class from `write_internal` up at **biometric** — and every approval a sandboxed
     run requests is **denied by the hub at once, never offered to the owner** (no Inbox card, no
     push); the node gets the denial and the action is recorded;
   - no vault writes (no write tools, `memory.writes` cleared), no skills, no review loop, one task
     per tree (sub-tasks are refused);
   - small budget and time cap: `tasks.sandbox.max_cost_eur` (default €0.20), `timeout` (5m),
     `max_runs` (2), enforced as tree limits with a deadline timer;
   - labelled and hidden: source `sandbox`, title `[sandbox] …`, params `sandbox: true`; not in
     `ListTasks` (reachable by id); no result notice.
   The test result gets the run's tool calls and **blocked actions** (removed tools and every denied
   action, e.g. `spend: Buy drill €89 (denied: sandbox test run)`) from `SandboxReport`.
2. `native_loop` — with an LLM (`architect.test_model`, else `model`, else `smart`): an in-process
   tool loop where read/compute hub tools really run and anything else (`vault.write`, `email.send`,
   purchases, deletes, node tools) is **refused** with `{"sandbox":"blocked"}` and listed.
3. `simulated` — otherwise: the agent-architect task is asked to role-play the draft
   ("SANDBOX SIMULATION"), calling only its own read tools.

No sandbox path can perform external, spend or destructive actions.

## Publishing (owner approval)

Agents can never write `agents/`, `policies/`, `workflows/` or `shops/` through the vault tools;
that protection is unchanged. The architect publishes through the **owner path** (the hub's
`AgentCatalog.UpsertAgent`, `TeamCatalog.UpsertTeam`, `Vault.Write`) and only after an explicit
approval:

1. `Publish(draft_id)` creates an Approval — class `architect.publish_class` (default
   `write_external`), level `architect.publish_level` or the policy's (at least *approve*; set
   `biometric` for Face ID) — whose **payload is the exact notes** (path, markdown, diff against
   the current note). The draft goes to PENDING_APPROVAL.
2. On **approved** (event bus, or reconciled at start-up) all notes are written, all-or-nothing
   (every writer is checked first), with commit message `architect: publish draft … (approval …)`;
   the draft becomes PUBLISHED and a notice is sent.
3. **Denied / expired**: nothing is written; the draft returns to READY with the reason. A decision
   on an older approval of the same draft is ignored.

## Learning from outcomes

`architect.tune` (hub tool, `write_internal`; weekly via `workflows/agent-tuning.md`) aggregates
the last 7 days of tasks per agent (TaskEngine.ListTasks) and the router's corrections. Agents with
≥ 3 tasks and ≥ 30 % failures, or ≥ 2 corrections away from them, get an improvement draft
(`update_agent_id`) whose context lists the failed-task summaries and where the owner re-routed
them (max 3 drafts; none while one is open for that agent). Drafts follow the same review, test
and approval flow.

## API summary (for clients)

`nexus.v1.ArchitectService` (proto/nexus/v1/architect.proto):

| RPC | Notes |
|---|---|
| `Draft{request, kind (auto/agent/team/workflow), context, wait, update_agent_id}` | → `AgentDraft` |
| `Refine{draft_id, feedback, wait}` | new revision |
| `TestRun{draft_id, cases?, wait}` | `draft.test_results[]` |
| `Publish{draft_id}` | → draft (PENDING_APPROVAL) + `Approval` (shows in the Inbox) |
| `ListDrafts{states?, limit}` / `GetDraft{id}` / `Discard{id}` | |

`AgentDraft`: `id, kind, state, request, context, notes[] {kind, id, path, markdown, diff, exists},
rationale, risks[], test_cases[], problems[], fixes[], test_results[] {name, input, passed, output,
checks[], blocked_actions[], tool_calls[], mode, task_id, error}, approval_id, task_id, via,
revision, feedback[], error, update_agent_id, created_at, updated_at`. Draft progress is announced
with Inbox notices (`kind: suggestion`, `data.draft_id`).

Hub tools: `architect.list_tools`, `architect.list_agents`, `architect.validate` (read; given to
agent-architect) and `architect.tune` (write_internal; for the tuning workflow — deliberately not
given to agent-architect).

## Configuration

```yaml
architect:
  mode: auto              # auto | llm | agent
  model: ""               # strong model alias for the llm path
  agent: agent-architect
  test_model: ""
  draft_timeout: 20m
  test_timeout: 10m
  publish_class: write_external
  publish_level: ""       # "" = policy (≥ approve) | approve | biometric
  tune_window: 168h
  tune_min_tasks: 3
  tune_fail_rate: 0.3
  tune_max_drafts: 3
```

Table: `architect_drafts` (module `architect`).
