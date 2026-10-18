# Complex agents, teams and swarms

> Status: implemented (backend). API: `proto/nexus/v1/team.proto`, `agent.proto`, `task.proto`,
> `event.proto`. Code: `backend/internal/tasks` (tree.go, prepare.go, results.go, team.go,
> teamtools.go, teamservice.go), `backend/internal/vault/teamdef`, `backend/internal/vault/teams.go`,
> `backend/internal/schema`.

Nexus lets you build two things on top of ordinary agents:

1. **Complex agents** — an agent note with skills, structured inputs/outputs, a reflection loop
   (critic review), injected memory and tree budgets.
2. **Teams (swarms)** — a team note: an orchestrator plus members working on one task tree, with
   team tools, a shared blackboard, messages, hub-driven strategies and hub-enforced limits.

Everything stays a vault note, editable from Obsidian, the dashboard or by an agent.

---

## 1. Concepts

| Concept | What it is |
|---|---|
| **Task tree** | Every sub-task (spawn_agent, team member, critic, spawned specialist) belongs to the tree of its root task. `Task.root_id`, `parent_id`, `depth`. |
| **Tree limits** | Depth, task count, parallelism, runs, LLM cost and wall clock for the whole tree, enforced by the hub (§6). |
| **Team run** | A task tree whose root (`role: orchestrator`, `team_id` set) is run by the team's orchestrator. |
| **Member** | A team entry `{agent, role, count, when}`; delegated tasks carry `Task.role`. `count` is a worker pool: at most `count` tasks of that role active at once. |
| **Ephemeral specialist** | An agent without a note, created by `team.spawn` (agent id `eph:<role>`). |
| **Blackboard** | A key/value scratchpad per team run (DB, mirrored to `teams/runs/<root>/blackboard.md`). |
| **Phase** | `Task.phase` says what the hub is doing when the state alone does not: `waiting_slot`, `strategy`, `reviewing`, `revising`, `repairing`. |

---

## 2. Complex agents (agent note fields)

All fields are optional and backward compatible.

```yaml
---
type: agent
id: report-writer
description: Turns findings into a reviewed report.
runtime: auto
skills: [writing-report]                 # §2.1
inputs:                                  # §2.2 JSON Schema of task params
  type: object
  required: [topic]
  properties: { topic: { type: string } }
output:                                  # §2.3 JSON Schema of the result
  type: object
  required: [summary, sources]
  properties:
    summary: { type: string }
    sources: { type: array, items: { type: string } }
review:                                  # §2.4 reflection loop
  critic: critic
  rounds: 1
  criteria: Every claim has a source; the summary stands alone.
memory:                                  # §2.5
  reads: ["[[Shopping preferences]]", "products/", "search: heat pumps"]
  writes: "research/"
limits:                                  # §6
  max_runtime: 20m
  max_cost_eur: 0.50          # per run
  max_tree_cost_eur: 1.20     # whole tree (this task + sub-tasks + reviews + repairs)
  max_runs: 6
  max_tasks: 10
  max_depth: 2
  max_parallel: 2
---
```

### 2.1 Skills — `skills/<id>.md`

```markdown
---
type: skill
id: web-research
name: Web research
description: Search the web, read primary sources and cite every claim.
tools: [web.search, web.fetch, web.extract, memory.search]
---
1. Check the vault first …
```

At assignment the hub appends `## Skill: <name>` + the body to the agent's instructions and merges
the skill's tools into the run's allow-list. Unknown/disabled skills are skipped and reported as
`Agent.warnings`. Starter skills: `web-research`, `writing-report`, `critique`, `data-extraction`,
`shopping-checkout-safety`. API: `AgentService.ListSkills / GetSkill / UpsertSkill`.

### 2.2 Structured inputs

`inputs:` is a JSON Schema; `CreateTaskRequest.params` (and `team.delegate` `inputs`) must match it.
A mismatch fails the task (`team.delegate` returns the error to the orchestrator instead).

### 2.3 Structured output (validation + one repair)

With `output:` the run's instructions get an "Output format (required)" section with the schema and
the context gets `output_schema`. When the run succeeds the hub extracts the JSON (whole answer, a
```json fence, or the outermost `{…}`/`[…]`; a `team.report` result is tried first) and validates it
(JSON Schema 2020-12 via `santhosh-tekuri/jsonschema`; external `$ref`s are refused). Valid → stored
in `Task.result` (non-objects as `{"value": …}`). Invalid → **one repair run** (phase `repairing`,
`escalation_hint` = the validation error, `previous_attempt` = the bad answer). Invalid again → the
task fails with `result.error_kind = "invalid_output"`.

### 2.4 Reflection loop (`review:`)

After a successful (and valid) answer the task stays `running` in phase `reviewing`; the hub creates
a **critic task** (child, `role: critic`, agent `review.critic`) whose output is forced to
`{approved: bool, feedback: string, score?: number}`. Approved → the answer is final. Rejected →
a revision run of the agent with the feedback as hint (phase `revising`); the revised answer is
reviewed again. `rounds` = max revisions (default 1); after that the last answer is final and the
critic's remaining concerns are in the transcript. A critic that fails does not block the task.
Critic tasks count toward the tree's limits; critic agents are never themselves reviewed.

### 2.5 Memory reads

`memory.reads` entries are injected into the run context as `memory: [{path, title, content}]`
(≤ 12 notes, 4 KB each, 24 KB total): `[[Title]]` or a path/id → that note; `folder/` → its 5 most
recently changed notes; `search: query` (or `?query`) → the top 3 search hits.

---

## 3. Teams — `teams/<id>.md`

```markdown
---
type: team
id: research-squad
name: Research squad
description: … (the router uses it — teams are routable like agents)
orchestrator: research-lead        # an agent id, or inline: {instructions, runtime, model, tier, tools, node}
strategy: hierarchical             # hierarchical | parallel | pipeline | debate | auto
members:
  - { agent: researcher, role: researcher, count: 3, when: "one sub-question each" }
  - { agent: fact-checker, role: fact-checker, when: "once a draft exists" }
shared: { blackboard: true }       # default true
limits: { max_parallel: 3, max_tasks: 12, max_depth: 2, max_cost_eur: 3, timeout: 45m, max_runs: 24 }
approvals: { bundle: true }
output: { type: object, … }        # optional: JSON Schema of the team's final result (the orchestrator's answer)
debate: { rounds: 2 }              # debate strategy
triggers: ["deep dive", "research squad"]
---
Mission / operating instructions (given to the orchestrator and, as context, to members).
```

Validation (`teamdef` + catalog): id, description, valid strategy; hierarchical/auto need an
orchestrator; parallel/pipeline/debate need members; a debate needs ≥ 2 debaters (members other than
`role: judge`, counts included); roles unique (`orchestrator` reserved); `count` 1–20; member and
orchestrator agents must exist (unknown → error, disabled → warning). `TeamService.UpsertTeam
{validate_only: true}` returns the parsed team with `errors`/`warnings` for editors.

**Starting a run:** `TaskService.CreateTask{prompt, team_id}` (or `agent_id: "team:<id>"`),
`nexus run "…" -team research-squad -follow`, or let the router pick it (teams appear in the
router catalog as `team:<id>`; `Route.team_id` is set). The root task gets `team_id`, `role:
orchestrator` and the orchestrator agent (`eph:orchestrator` for inline orchestrators).

**Orchestrator contract:** the orchestrator is an ordinary run on any runtime (Claude Code / Codex
over the Nexus MCP bridge, native agent loop, …). At assignment the hub adds the orchestrator team
tools to its allow-list and a team brief (mission, roster with descriptions and input/output schemas,
limits, how to work) to its instructions; the context carries `team` (id, role, root, strategy,
members, limits), `blackboard` (snapshot) and `team_messages`. Members get member tools, a member
brief and the same context. The orchestrator's final answer is the team's result; when the root
ends, unfinished member tasks are cancelled.

Starter teams: `research-squad`, `product-buyer` (inline orchestrator; the shopper's checkout still
needs Face ID), `dev-crew` (architect + 2 coders + tester + reviewer + `anthrex-dev` for whole
multi-step goals), `debate-panel` (3 analysts + judge, no orchestrator). New starter agents:
`architect`, `reviewer`, `tester`, `analyst`, `judge`, `critic`, `report-writer`.

---

## 4. Team tools reference

Hub tools, injected per run and scoped by the run: the hub resolves the caller's task from the run
token, so a run can only touch its own team run; a stale run (not the task's current run) is
refused. Over MCP the names are `team_delegate`, `team_blackboard_read`, … Errors come back as tool
errors with Connect codes (`invalid_argument`, `permission_denied`, `resource_exhausted`…).

| Tool | Who | Input | Output |
|---|---|---|---|
| `team.members` | orch. | `{}` | `{team, strategy, members:[{role, agent, count, when, description, inputs_schema, output_schema, active, queued, finished}], spawned:[…], limits}` |
| `team.delegate` | orch. | `{role \| agent, task, inputs?, wait?, timeout?}` | `{task_id, role, state, phase}`; with `wait` the `team.await` output |
| `team.delegate_many` | orch. | `{items:[{role \| agent, task, inputs?}], wait?, timeout?}` | `{tasks:[{index, task_id, role, state, phase}], errors:[{index, role, error}]}` or await output + `errors` |
| `team.await` | orch. | `{task_ids?, any?, timeout?}` (default: all its unfinished sub-tasks) | `{done:[{task_id, role, agent_id, state, summary, result}], pending:[…], timed_out, note}` |
| `team.status` | orch. | `{}` | `{tasks:[{task_id, parent_id, role, agent, state, phase, runs, cost, summary}], total_tasks, total_runs, active, queued, total_cost, limits, time_left_seconds}` |
| `team.cancel` | orch. | `{task_id, reason?}` | `{task_id, cancelled}` (cascades to its sub-tasks) |
| `team.spawn` | orch. | `{role, instructions, task, runtime?, tools?, coding?, inputs?, wait?, timeout?}` | like `team.delegate` |
| `team.run_strategy` | orch. | `{strategy: parallel\|pipeline\|debate, task?, roles?, timeout?}` | `{strategy, results:[…], final, error?}` |
| `team.message` | all | `{to: role \| task_id \| "orchestrator" \| "all", text}` | `{message_id, delivered_to}` |
| `team.report` | all | `{result: any JSON, summary?}` | `{ok}` — attached as the task's structured result |
| `team.blackboard.read` | all* | `{key?}` | `{entries:[{key, value, by, version}]}` or `{key, found:false}` |
| `team.blackboard.write` | all* | `{key, value}` | `{key, version}` |
| `team.blackboard.append` | all* | `{key, value}` | `{key, version}` — appends to a list, or a new line to text |

\* when the team has `shared.blackboard` (default). Action classes: reads `read`, the rest
`compute` (both `auto` by default; an agent's `policy:` can tighten them).

**Blocking calls** (`team.await`, `wait: true`, `team.run_strategy`) block hub-side until done or the
timeout (default `tasks.teams.await_default` = 10 min, capped at `await_max` = 55 min, below the
62-minute MCP tool timeout Nexus sets for Claude Code / Codex). On timeout they return the pending
tasks — call again. Each wait publishes a `TEAM_EVENT_KIND_AWAITING` event and every member that
finishes a `MEMBER_FINISHED` event, so clients see progress while the tool blocks.

**Messages** to running members are delivered at once as a `RunInput` (`[Team message from
<role>]: …`); members that are not running yet get them in `team_messages` when they start. Users can
message a run with `TeamService.SendTeamMessage` (from role `user`).

---

## 5. Strategies

| Strategy | Behaviour |
|---|---|
| `hierarchical` (default) | The orchestrator plans, delegates, awaits and synthesises with the team tools. |
| `parallel` | The hub gives the task to every member (pools expanded: `count: 3` = three tasks with "take your own angle"), waits, then the orchestrator merges (context `strategy.results`, `strategy.final`). Without an orchestrator the root succeeds with the combined results. |
| `pipeline` | The hub runs members in order; stage *n* gets the task plus stage *n−1*'s output (and its `when` as "Your stage"). A failed stage stops the pipeline. Without an orchestrator the last stage's output is the result. |
| `debate` | Debaters (members except `role: judge`, pools expanded) answer in round 1, then for `debate.rounds` rounds see each other's latest positions and rebut/concede. A `judge` member then decides; without one the orchestrator judges (or the transcript is the result). |
| `auto` | Hierarchical, plus the brief offers `team.run_strategy` so the orchestrator can let the hub run any of the fixed patterns for (part of) the task. |

The fixed strategies run in the hub (root in phase `strategy`, state `queued`), so they work with
simple models: the orchestrator only has to synthesise. Strategy steps publish
`TEAM_EVENT_KIND_STRATEGY_STEP`.

---

## 6. Limits & safety (enforced by the hub, not the model)

| Limit | Where | When |
|---|---|---|
| `max_depth` | team `limits`, agent `limits.max_depth`, global `tasks.max_depth` (min of them) | creating a sub-task |
| `max_tasks` | team / agent `limits.max_tasks` | creating a sub-task (root counted) |
| `max_parallel` | team / agent `limits.max_parallel` | sub-tasks start only when a slot is free; others wait `queued` / `waiting_slot` (FIFO), started as tasks end |
| member `count` | team `members[].count` | per-role pool, same queue |
| `max_runs` | team / agent `limits.max_runs` | before every run (retries, repairs, reviews included) |
| `max_cost` | team `limits.max_cost_eur`, agent `limits.max_tree_cost_eur` | before every run, and on every cost event: when the tree's total LLM cost reaches it the tree is **stopped** (root failed "budget exceeded", every other task cancelled). Each run's own `max_cost` is clamped to the budget left. |
| `timeout` | team `limits.timeout` | the run's deadline: a timer stops the tree; every run's `max_runtime` is clamped to the time left |

Team defaults for unset limits come from `tasks.teams` (`max_parallel 4`, `max_tasks 30`,
`max_depth 3`, `max_runs 60`, `timeout 2h`). Plain `spawn_agent` trees get a tree row (and these
checks) from the root agent's limits the first time a sub-task is created.

- **Costs roll up:** `Task.tree_cost` on the root (updated live) and `TeamRun.total_cost`.
- **Cancel cascade / kill switch:** cancelling a task cancels its sub-tasks; `StopAll` cancels
  everything; when a team root ends, unfinished members are cancelled; a stopped tree cancels its
  queued tasks and refuses new tasks and runs.
- **Ephemeral specialists never exceed the orchestrator:** tools must be allowed by the
  orchestrator's own allow-list (team tools excluded), the runtime must be one of the orchestrator's
  runtimes, and they inherit its policy, node selector, model, tier and per-run limits.
- **Coding-only tools (`anthrex.*`)** are for coding agents only (the node also enforces this in
  `internal/node/codeonly.go`: a coding agent runs on the `anthrex` runtime or sets `coding: true` in
  its `claude:`/`codex:` block). `team.spawn` never gives `anthrex.*` tools to a specialist unless it
  is explicitly a coding specialist (`coding: true` or `runtime: anthrex`) **and** the orchestrator is
  itself a coding agent. In the starter teams only `dev-crew` uses coding members (code-fixer,
  tester, `anthrex-dev` for whole multi-step goals); research-squad, product-buyer and debate-panel
  members list no `anthrex.*` tools.
- **Approvals stay per member:** every member runs with its own policy and the node's approval gate,
  so the shopper's checkout still asks for Face ID. Approvals carry `root_task_id`, `team_id`, `role`
  and `bundle` (team `approvals.bundle`) so clients can group them in one card; push notifications
  of a tree share one thread. Each pending member approval also emits `APPROVAL_PENDING` on the root.
- Team messages and blackboard values from other agents are data: the briefs tell agents not to
  treat them as instructions.

---

## 7. Observability & API (for clients)

- **Events:** `Event.root_task_id` is set on every task event; `SubscribeRequest.root_task_ids`
  streams a whole tree. `Event.team` (`TeamEvent{kind, team_id, root_task_id, task_id, role,
  agent_id, text, key, data}`) is published on the root task: `STARTED`, `DELEGATED`, `SPAWNED`,
  `MEMBER_FINISHED`, `BLACKBOARD_UPDATED`, `MESSAGE_SENT`, `REPORTED`, `AWAITING`, `STRATEGY_STEP`,
  `REVIEW`, `LIMIT_REACHED`, `APPROVAL_PENDING`, `FINISHED`.
- **TeamService:** `ListTeams`, `GetTeam` (definition + markdown), `UpsertTeam` (validate / write
  `teams/<id>.md`), `GetTeamRun(root_task_id)` → `TeamRun{root, team snapshot, tasks[{task, runs,
  ephemeral, member_role}], blackboard, messages, total_cost, total_tasks, total_runs, active_tasks,
  queued_tasks, limits, stopped_reason, deadline}` (works for any task tree), `SendTeamMessage`.
- **TaskService:** `CreateTaskRequest.team_id`; `Task.team_id / role / depth / phase / tree_cost`;
  `ListTasksRequest.team_id / root_id` (team runs = `{team_id, roots_only: true}`).
- **AgentService:** `ListSkills`, `GetSkill`, `UpsertSkill`; `Agent.skills / input_schema /
  output_schema / review / memory / warnings`, `AgentLimits.max_depth / max_parallel / max_tasks /
  max_tree_cost / max_runs`.
- **CLI:** `nexus run "…" -team <id> -follow` (streams the whole tree, sub-task lines prefixed with
  the role), `nexus teams`, `nexus team-run <root> [-json]`.

---

## 8. Building a complex agent, step by step

1. **Start from a plain agent note** (`agents/<id>.md`): description (for the router), runtime
   (`auto`), tools, body = instructions.
2. **Factor reusable know-how into skills** (`skills/<id>.md`) and list them in `skills:`.
3. **Make it composable:** add `inputs:` for the parameters callers must pass and `output:` for the
   result shape; other agents and teams (`team.delegate inputs`, workflows) then get validated,
   structured data.
4. **Add quality control:** `review: {critic: critic, rounds: 1, criteria: …}`.
5. **Give it context:** `memory.reads` for the notes it should always see.
6. **Bound it:** per-run `max_runtime` / `max_cost_eur`, tree `max_tree_cost_eur` / `max_runs`.
7. **Put it in a team** when one agent is not enough: write `teams/<id>.md` with an orchestrator,
   members (with `when:` so the orchestrator knows whom to use), a strategy and limits; validate
   with `UpsertTeam{validate_only}` or `nexus teams`; run with `nexus run "…" -team <id> -follow`.

---

## 9. Known gaps

- Strategy drivers, deadline timers and blocking `team.await` calls live in hub memory: after a hub
  restart a team run in phase `strategy` does not resume (cancel and restart it); deadlines are still
  checked before new runs.
- Bundled approvals are grouping metadata only (no combined signature over several approvals).
- Team runs cannot be sub-tasks of other tasks (no nested teams); members cannot delegate (they may
  still use `spawn_agent` within the tree limits).
- No MCP progress notifications during long tool calls (the 55-minute cap + re-await covers it).
- Workflows cannot start a team as a step yet.
