# Agents, Runtimes and Routing

> Status: draft v0.1

## 1. What an agent is

An **agent** is a vault note in `agents/` — YAML frontmatter for configuration, Markdown body for its
instructions and playbook. Creating an agent is writing a note (from Obsidian, the web Agent Studio,
the iOS app, or by another agent with approval).

```markdown
---
type: agent
id: grocery-continente
name: Grocery (Continente)
description: Buys groceries on continente.pt from a list and guidelines. Use for shopping lists,
  supermarket orders, "buy the usual".
icon: cart
runtime: openai              # claude-code | codex | openai | ollama | anthrex | shell | browser | workflow
model: smart                 # model alias resolved by policies/models.md
tier: medium                 # simple | medium | heavy  (used for routing + escalation)
node:
  tags: [browser, always-on] # node selector
tools:
  - browser
  - memory.search
  - vault.write
  - ask_user
  - request_approval
limits:
  max_runtime: 30m
  max_cost_eur: 0.50         # LLM spend per run
  max_spend_eur: 150         # real-world money per run
policy:
  spend: biometric
  substitutions: approve
memory:
  reads: ["[[Shopping preferences]]", "products/"]
  writes: "episodes/shopping/"
triggers:
  - phrase: ["groceries", "compras", "lista de compras", "buy the usual"]
version: 3
---

# Grocery (Continente)

## Goal
Fill a Continente cart that matches the list and the guidelines, then ask for approval to check out.

## Playbook
1. Resolve each list item against [[Shopping preferences]] and `products/` notes.
2. ...

## Lessons learned
<!-- appended by the agent after each run -->
```

### Fields

| Field | Meaning |
|---|---|
| `runtime` | Which adapter executes it (see §2). |
| `model` | Alias, not a provider model id. Aliases live in `policies/models.md` and map to local (Ollama) or cloud models. |
| `tier` | Cost/difficulty class. Drives routing and escalation. |
| `node` | Selector: tags, explicit node id, or `any`. |
| `tools` | Allow-list. Runtimes that bring their own tools (Claude Code, Codex) also get these via MCP. |
| `limits` | Time, LLM cost, real spend, max sub-tasks, max tokens. |
| `policy` | Per-agent overrides of trust levels (cannot be looser than global policy unless global allows). |
| `memory` | What the agent is given as context by default, and where it writes episodes. |
| `triggers` | Phrases/patterns used by the rule stage of the router; schedules and events live in workflows. |
| body | System instructions + playbook. "Lessons learned" is appended by the agent (procedural memory). |

Agent notes are versioned by git; the Agent Studio shows diffs and lets you roll back.

---

## 2. Runtimes

All runtimes implement one Go interface on the Node:

```go
// internal/runtime/runtime.go (sketch)
type Runtime interface {
    Name() string
    Capabilities() Capabilities // streaming, steering, native tools, approvals hook, cost reporting
    Start(ctx context.Context, spec RunSpec) (Run, error)
}

type Run interface {
    Events() <-chan Event                                   // text, tool_call, tool_result, ask, approval_request, cost, artifact, done
    Send(ctx context.Context, in Input) error                 // user message / answer to a question
    Resolve(ctx context.Context, approvalID string, d Decision) error
    Cancel(ctx context.Context) error
}
```

| Runtime | Best for | How Nexus drives it |
|---|---|---|
| `claude-code` | Coding, repo work, long multi-step tasks on a machine | Headless `claude -p` with streaming JSON in/out; Nexus MCP server attached; permission prompts routed to Nexus approvals via a permission-prompt tool. |
| `codex` | Coding, second opinions, parallel attempts | `codex exec` non-interactive with JSON events; sandbox/approval mode set from agent policy; Nexus MCP attached. |
| `openai` | General agents on any OpenAI/Anthropic-compatible model (local Ollama or cloud) | Native Go agent loop (tool calling) over OpenAI-compatible API. |
| `ollama` | Cheap/private simple work: routing, tagging, summarizing, extraction | Same native loop, local endpoint. |
| `anthrex` | Whatever Anthrex specializes in | Adapter over Anthrex's interface (TBD — see OPEN_QUESTIONS). Anthrex repo is not modified. |
| `shell` | Scripts, ops, deterministic jobs | Command templates with typed params; stdout/stderr streamed. |
| `browser` | Sites without APIs (Continente, portals) | Playwright with persistent profiles per site on the Node; driven by an LLM loop with a page-snapshot tool; screenshots as artifacts. |
| `workflow` | Composite agents | The agent is a workflow (see WORKFLOWS.md). |

### The native agent loop (`openai` / `ollama`)

Nexus ships its own small, fast agent loop in Go: messages + tool schema → model → tool calls →
policy check → execute (locally on the Node or proxied to the Hub) → repeat until done or limits hit.
Every step emits events, so the phone sees exactly what is happening.

### Nexus tools available to every agent

| Tool | What it does |
|---|---|
| `memory.search` | Hybrid search (FTS + vectors + graph neighbours) over the vault. |
| `vault.read` / `vault.write` / `vault.link` | Read and write notes; writes are policy-checked and committed. |
| `ask_user` | Ask a question with optional choices → push notification → answer flows back. |
| `request_approval` | Explicit approval for an action with a preview payload. |
| `notify` | Send a result/notice to the Inbox (no response needed). |
| `spawn_agent` | Create a sub-task for another agent (by id or by description → router). |
| `node.exec` / `node.files` | Run commands / move files on a (permitted) node. |
| `schedule` | Create a reminder/follow-up task in the future. |
| integration tools | email, calendar, GitHub, Home Assistant… (when connected and allowed). |

---

## 3. Model aliases and tiers

`policies/models.md` maps aliases to concrete endpoints so agents never hard-code providers:

```yaml
aliases:
  fast-local:  { provider: ollama,  model: "<small local model>", node: gpu-box }
  smart:       { provider: ollama,  model: "<model name>" }
  best:        { provider: ollama,  model: "<model name>" }
  embed:       { provider: ollama,  model: "<embedding model>" }
tiers:
  simple: [fast-local, smart]
  medium: [smart, best]
  heavy:  [claude-code, codex]
```

---

## 4. Router

The router answers: *what is this input, and who should handle it?*

```mermaid
flowchart LR
  IN[Input + context<br/>source, location, focus, attachments] --> K{Intent kind}
  K -->|question about my stuff| S[Search / answer from vault]
  K -->|remember this| C[Capture to vault]
  K -->|do something| A[Agent selection]
  K -->|matches workflow| W[Start workflow]
  A --> R1[1 · Rules<br/>phrases, share-type, URL domains]
  R1 -->|no match| R2[2 · Embedding similarity<br/>vs agent descriptions]
  R2 --> R3[3 · Local LLM classifier<br/>top-k candidates]
  R3 -->|low confidence| R4[4 · Cloud model]
  R4 -->|still unsure| ASK[Ask user: A or B?]
```

Output:

```json
{ "kind": "task", "agent": "grocery-continente", "tier": "medium",
  "confidence": 0.91, "params": { "list": [...], "deadline": "saturday" },
  "reason": "shopping list shared from Notes" }
```

- **Every decision is logged** (input, candidates, choice, confidence, latency, cost).
- **Corrections are training data:** when you change the agent from the phone ("no, use X"), the
  pair is stored. The dashboard's Router page shows accuracy and lets you export a dataset.
- **Fine-tuned router (later):** a small local model fine-tuned on these logs becomes stage 3;
  the cloud stage becomes the fallback.

### Escalation

A run escalates to the next tier when: the model reports low confidence, a verifier check fails
(schema validation, tests, a judge prompt), the run exceeds its step budget without progress, or the
user taps **"Try harder"**. Escalation is a new run on the same task; history is passed along.

### Parallel attempts

For important tasks an agent can be configured with `strategy: race` (first good result wins) or
`strategy: compare` (e.g. Claude Code and Codex both attempt; you pick on the phone).

---

## 5. Multi-agent orchestration

- **Planner agents** decompose a request into sub-tasks (`spawn_agent`), wait, and combine results.
- Tasks form a **tree**; the UI shows it live and costs roll up.
- **Bundled approvals:** a planner can collect its children's approvals into one decision card.
- Guardrails: max depth, max children, total budget per root task.

---

## 6. Learning agents (procedural memory)

After a run, the agent (or a cheap reviewer model) appends to **Lessons learned** in its note:
selectors that changed, user corrections, faster paths. Changes are committed with the run id, so
any lesson can be traced and reverted. Repeated identical corrections trigger a suggestion to change
the agent's instructions or policy.

---

## 7. Trust and autonomy

Every tool call is classified into an **action class** and checked against policy:

| Action class | Examples | Default level |
|---|---|---|
| `read` | search, read files, browse | auto |
| `write_internal` | write vault notes, create tasks | auto / notify |
| `compute` | run code in a workspace | auto (sandboxed) |
| `write_external` | send email/messages, post, create PR | approve |
| `code_push` | push to a remote branch | approve |
| `spend` | purchases, paid APIs over budget | biometric |
| `destructive` | delete files, drop data, force-push | biometric |
| `device_control` | smart home, wake/sleep machines | notify / approve |

Levels: **auto** (just do it) → **notify** (do it, tell me) → **approve** (wait for a tap) →
**biometric** (Face ID + signed approval).

**Earned autonomy:** after N approvals of the same agent+action pattern with no denials/corrections,
Nexus suggests lowering the level for that pattern. You always confirm the change.
