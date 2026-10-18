# Anthrex in Nexus

[Anthrex](https://github.com/danielpina1/anthrex) is the owner's Rust daemon for coding agents: a
terminal multiplexer for Claude Code / Codex / shell windows **and** a deterministic orchestration
engine that turns a goal into a plan of tasks, runs workers in git worktrees, checks, reviews and
merges them into a run branch, and finally merges (or opens stacked PRs) after a human decision.

Nexus does not reimplement any of that. The node module **`anthrex`** talks to the local Anthrex
daemon and maps its runs, gates and windows onto Nexus tasks, approvals, questions and tools.
The Anthrex repository is never modified by Nexus.

```
iPhone / web  ──Approval / Question──▶  Hub  ◀──RunEvents──  nexus-node (Mac)
                                                                 │  module "anthrex"
                                                                 │  runtime "anthrex" + anthrex.* tools
                                                                 ▼
                                               Unix socket (MessagePack, proto 21)
                                                                 ▼
                                                         anthrex daemon ── claude / codex workers
```

## 1. What maps to what

| Anthrex | Nexus surface |
|---|---|
| `RunRequest::StartGoal` (goal, dir, delivery, design, orchestrator) | A task for an agent with `runtime: anthrex` (e.g. `anthrex-dev`); the task prompt is the goal, `params.repo_dir` / the agent's `anthrex.repo` the repository. Tool `anthrex.start_goal`. |
| `RunReply::Queued` (repository has no profile yet) | Run output "goal queued"; Nexus follows `RunsSnapshot.queued_goals` (`needs_review` → hint to run `anthrex profile confirm`; `failed` → task FAILED) and adopts the run once it starts. |
| Runs snapshot (`RunRequest::Subscribe` pushes) | The Nexus run mirrors it: run-state lines and attention lines as `RunOutput`; each Anthrex task → one `ToolCall` (`anthrex.task`, input: id, title, kind, size, role, route) and a `ToolResult` when it is merged / reported / cancelled (tokens, diff, merge commit); task transitions as output lines. |
| `RunInfo.usage` (token usage by role) | `CostRecorded` events (input = input + cache_write, output; runtime `anthrex`, model = orchestrator model), at most every 15 s; the total is the task's cost. |
| Plan gate (`awaiting_approval`) → `Approve` / `Reject` | **Approval** (class COMPUTE, raised to APPROVE on this runtime): the plan's tasks with routes. Approve → `Approve`; deny → `Reject`. `anthrex.plan_approval: auto` starts goals with `--yes` instead. |
| Approval holds (`HoldInfo` awaiting) → `ApproveHold` / `RejectHold` | **Approval** per hold (class COMPUTE→APPROVE). |
| Design gates (`doc_gate`: brainstorm / spec / plan) → `DocGate{approve,changes,back,rethink,reject}` | **Approval** with the document text (from `ShowDoc`). Approve → `DocGate approve {version}`. Deny → **Question** "changes / back (rethink at brainstorm) / reject", free text = the change note → `DocGate changes {note}` etc. |
| Orchestrator `ask_user` (`OrchestratorInfo.ask`) → `AnswerAsk` | **Question** with the options as choices. A choice → `AnswerAsk{choice: i}`; free text is typed into the orchestrator's window and then `AnswerAsk{choice: None}` (exactly what the Anthrex TUI does on Enter). |
| User-only halt (`halt_user_only`) → `Resume` / `Cancel` | **Question** "resume / resume with rebaseline / cancel". |
| Run `complete` → `Finish{accept}` → `ConfirmNeeded` → `Finish{accept, confirm}` | **Approval, class CODE_PUSH** (APPROVE by default; `policy: {code_push: biometric}` = Face ID): branch → base, tasks done, diff totals, failing checks, attention lines, REPORT.md excerpt; payload hashes run id, branches, base sha and run head. A moved base (`base_moved`) needs a second CODE_PUSH approval listing the new commits, then `confirm = "<run>@<new head>"`. Deny → Question keep / discard; discard is a separate **DESTRUCTIVE** approval → `Finish{discard}`. |
| `accepted` / `discarded` / `failed` | Terminal `TaskUpdated` SUCCEEDED / CANCELLED / FAILED. Summary = one line + **REPORT.md** (bounded by `report_max_bytes`), so the vault episode captures it; `REPORT.md` is also an artifact; `result` = run id, state, goal, title, branches, head, tasks (id, title, state, role, tokens), usage, report. |
| Nexus Cancel | `RunRequest::Cancel`, task CANCELLED. |
| Nexus message to a running task | Typed into the live orchestrator window, else `Edit[Message{to: running}]`. |
| Windows (`ListWindows`, `CreateWindow`, `Input`, `ConversationSnapshot`) | Tools `anthrex.windows`, `anthrex.new_window`, `anthrex.send_input`, `anthrex.conversation`. Client library also streams PTY bytes (`Supervisor.StreamWindow`) for a future web terminal (§6). |
| `Profile(Status)`, `Stats` | Tools `anthrex.profile`, `anthrex.stats`. |
| Gate decided in Anthrex itself (TUI / CLI) | The pending Nexus approval/question is abandoned (task back to RUNNING); a late decision on it is ignored. |
| PR delivery (`delivery: pr`) | Started with `--delivery pr`; no accept gate (Anthrex refuses accept in pr mode); the task ends when the run reaches a terminal state. |

### Node tools

Registered on the node (`h.Tools()`), so any agent whose allow-list names them can call them —
native agents, Claude Code and Codex through the Nexus MCP bridge, and team members.

| Tool | Class (default level) | What |
|---|---|---|
| `anthrex.status` | read (auto) | All runs (brief + pending decisions with the tool to call) or one run in full. |
| `anthrex.start_goal` | write_external (**approve**) | Start a goal in an allowed repo. |
| `anthrex.message` | write_internal (notify) | `PlanEdit::Message` to tasks / `stage:<n>` / `running`, or `to: orchestrator` (typed into its window). |
| `anthrex.approve` | write_internal (notify) | Plan approve/reject, hold approve/reject, design gate approve/changes/back/rethink/reject. |
| `anthrex.answer` | write_internal (notify) | Orchestrator `ask_user` (option number/text or free text) or a blocked task's question (`PlanEdit::Answer`). |
| `anthrex.accept` | code_push (**approve**; biometric per policy) | Two-step `Finish{accept}`; a moved base fails with the new head until called again with `base`. |
| `anthrex.cancel` | compute (auto) | Cancel a run. |
| `anthrex.windows` | read | List windows. |
| `anthrex.new_window` | compute (auto) | Window (claude / codex / shell) in an allowed repo, optional worktree branch and prompt. |
| `anthrex.send_input` | write_external (**approve**) | Type into a window (Enter appended unless `enter: false`). |
| `anthrex.conversation` | read | Last N turns of a window's conversation. |
| `anthrex.profile`, `anthrex.stats` | read | Profile status / history stats of an allowed repo. |

`start_goal` and `send_input` are compute-like, but their effects leave Nexus (agents editing a
repository; keystrokes into a terminal that may be a shell), so they use `write_external`, whose
default is APPROVE. The policy engine can only tighten these per agent.

## 2. Setup on the Mac

1. Install Anthrex (a release binary — see Anthrex's `docs/install.md` — or from a clone:
   `cargo install --path crates/cli`, giving `anthrex` on PATH), with `claude` and/or `codex`
   installed and logged in, and start the daemon (`anthrex daemon start`; `anthrex daemon status`).
2. In each repository you want Nexus to use, let Anthrex learn it: `anthrex profile detect` then
   `anthrex profile show --proposed` and `anthrex profile confirm`. (A goal in a repo without a
   profile is queued by Anthrex until this is done; Nexus waits and tells you.)
3. Configure the node (`node.yaml`) — `allowed_repos` is required; without it every goal and
   window request is refused with a message saying so:

   ```yaml
   anthrex:
     allowed_repos: [~/src/nexus, ~/src/anthrex-playground]
     default_delivery: local       # or pr
     design: off                   # or full
     # default_orchestrator: claude
     # plan_approval: approve      # or auto (= --yes: no plan gate)
     # socket: ""                  # default: ANTHREX_SOCKET, else Anthrex's own path rule
     # bin: anthrex                # CLI fallback
     # mode: auto                  # auto | socket | cli
   ```

4. Give the node the `dev` tag (the templates `anthrex-dev` and `anthrex-design` select
   `node: {tags: [dev]}`) and restart `nexus-node`. The node reports runtime `anthrex` as
   available only when the daemon answers the handshake (or the CLI fallback is installed).
5. To let the hub forward the agent note's `anthrex:` block to the node, `"anthrex"` must be in
   `internal/tasks.ForwardedAgentKeys` (owned by the tasks module; see "Integration notes").
   Until then the node config defaults apply, and `params.repo_dir` on the task selects the repo.

Socket discovery (same rules as `crates/proto/src/paths.rs`): `anthrex.socket` →
`$ANTHREX_SOCKET` → macOS `$TMPDIR/anthrex-<uid>/daemon.sock` (`/tmp` when `TMPDIR` is unset) →
Linux `$XDG_RUNTIME_DIR/anthrex/daemon.sock`, else `/tmp/anthrex-<uid>/daemon.sock`. The node must
run as the same user as the daemon (the socket directory is 0700).

### Using it from a team

`dev-crew` (or any orchestrator) delegates a coding goal to Anthrex by delegating to the agent
`anthrex-dev` (or `anthrex-design`) like to any member — `team.delegate` / `spawn_agent` with the
goal as prompt and `params: {repo_dir: ~/src/…}`. The member task stays open until you merge (or
discard) and returns the REPORT.md summary to the team. A team agent can also hold the
`anthrex.*` tools directly to watch runs (`anthrex.status`) or answer routine questions
(`anthrex.answer`) while the merge itself always stays a CODE_PUSH approval for you.

## 3. Safety

- **Local only.** The daemon listens on a Unix socket in a 0700 directory, no network, no auth
  beyond file permissions. Nexus connects as the same user on the same Mac; nothing is exposed on
  the hub. Do not point `anthrex.socket` at a shared location.
- **Repositories are allow-listed.** Goals, windows, profile and stats requests are refused
  outside `anthrex.allowed_repos` (paths are resolved through symlinks and `..`).
- **Merges are approvals.** The final accept is a CODE_PUSH approval bound (by payload hash) to
  the run id, branches, base sha and run head; a moved base needs a second approval naming the new
  commits. Set `policy: {code_push: biometric}` on the agent to require Face ID. Discard is
  DESTRUCTIVE (biometric by default).
- **Plan / hold / document gates** are approvals at APPROVE level on this runtime (COMPUTE is
  raised for its gate); agents may raise them to biometric, never lower them.
- Anthrex's own guarantees still apply (worktrees, checks, reviews, confinement of checks where
  the platform supports it). Nexus never passes `--unconfined-checks`. `trust_project` is off
  unless configured.

## 4. Protocol and version policy

- Wire: 4-byte big-endian length + MessagePack body (`rmp_serde::to_vec_named`: named fields,
  serde's externally tagged enums — unit variants as strings, struct/newtype variants as single-key
  maps; internally tagged enums such as `HoldKind`, `ActionKind`, `PlanEdit` as maps with a tag
  key), max 16 MiB per frame. Handshake: `Hello{proto_version: 21, client: "cli"}` →
  `Welcome{daemon_version, windows}` or `Error{request: "hello"}` within 5 s.
- Requests use `RunTagged{id, request}`; replies echo `request_id`. `List`/`Subscribe` are
  answered by (untagged) `Snapshot`s. Window requests (`CreateWindow`, `Kill`, `Input`…) are
  matched by their reply label (`create`, `kill`, `input`…); one `CreateWindow` is in flight at a
  time. A PTY view or conversation subscription gets its own connection.
- **Supported version: 21** (`anthrex.ProtoVersion`). The daemon refuses any other version at
  `Hello`; the client then returns `ErrProtoVersion` and `Open` falls back to the **CLI** (`anthrex
  run status --json`, `run start --goal … --yes --delivery …`, `run approve/reject/message/cancel/
  accept/discard`, `new`, `ls --json`, `profile status --json`, `run stats --json`), which always
  matches its own daemon. In CLI mode runs are polled every 3 s; `AnswerAsk`, task answers,
  `TaskDetail`, terminal input, PTY streams and conversations are unavailable (the runtime then
  tells the user to answer in Anthrex).
- Decoding is lenient: unknown fields are ignored and missing `#[serde(default)]` fields keep zero
  values, so additive changes within a version do not break us. When Anthrex bumps the version:
  re-copy the fixtures (`backend/internal/anthrex/testdata/README.md`), review `lib.rs`'s
  changelog for changed shapes of the messages in `wire.go` / `daemonmsg.go` / `types.go`, then add
  the version to `SupportedProtoVersions` and `ProtoVersion`.

Code: `backend/internal/anthrex` (codec, client, CLI fallback, typed views, fake daemon in
`anthrextest`), `backend/internal/runtime/anthrex` (module, runtime, tools).

## 5. Limits

- One Anthrex daemon per node, same user; no remote Anthrex.
- Free-text answers to the orchestrator are typed into its PTY window (the orchestrator is an
  interactive session); if it has no live window, answer in Anthrex.
- PR delivery is started and followed, but its stacked-PR lifecycle (CI, review threads) is not
  mirrored beyond run output and attention lines.
- `Iterate`, `Promote`, `Retry`, `Override`, plan `Edit` beyond messages/answers, and settings are
  not exposed as tools yet (the client builds `Retry`, `Override`, `Promote`, `Resume`).
- A Nexus approval that expires leaves the Anthrex gate open; decide it in Anthrex
  (`anthrex run approve …`) or re-run the task.

## 6. Next step: web terminal (hub RPC, not implemented yet)

Proposed `proto/nexus/v1/anthrex.proto`, proxied hub → node over the node link control channel:

```proto
service AnthrexService {
  rpc ListWindows(ListAnthrexWindowsRequest) returns (ListAnthrexWindowsResponse); // node_id
  rpc StreamWindow(StreamAnthrexWindowRequest) returns (stream AnthrexPtyChunk);  // node_id, window_id, cols, rows → {snapshot, cols, rows, bytes}
  rpc SendInput(SendAnthrexInputRequest) returns (SendAnthrexInputResponse);       // gated as write_external
  rpc ListRuns(ListAnthrexRunsRequest) returns (ListAnthrexRunsResponse);          // RunBrief list
}
```

The node side already exists in the client library: `Supervisor.StreamWindow` (Snapshot then
Output bytes, resynchronising snapshots on lag), `SendInput`, `ListWindows`, `ListRuns`. Missing:
the nodelink request/stream plumbing and the web terminal (xterm.js) in `web/`.

## Integration notes (for other owners)

- `internal/tasks/agentconfig.go`: add `"anthrex"` to `ForwardedAgentKeys` so the agent note's
  `anthrex:` block (repo, delivery, design, orchestrator, plan_approval) reaches the node.
- `docs/AGENTS.md` §2's `anthrex` row ("TBD") can now point here.
