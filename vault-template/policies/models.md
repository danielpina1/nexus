---
type: policy
id: models
name: Model aliases and runtimes
# Runtime preferences per tier, for agents with `runtime: auto`. The hub picks
# the first runtime that a connected node (matching the agent's node tags)
# reports as available. When a run stops on a usage limit (or a CLI that is
# not logged in), the task is retried on the next runtime of the list.
#   claude-code = Claude Code CLI, logged in with your Claude Pro/Max subscription
#   codex       = OpenAI Codex CLI, logged in with your ChatGPT Plus/Pro subscription
#   ollama      = Nexus's native agent loop on a local Ollama model (free, private)
# Default: subscriptions only (Claude Code, then Codex); no local models run.
# Simple work uses Claude Code with the fast-local alias (Haiku). To use a
# local Ollama model too, add `ollama` to a list, e.g. simple: [ollama, codex, claude-code],
# and enable the Ollama provider in nexus.yaml / node.yaml.
# An agent can set its own order with `runtimes: [codex, claude-code]`.
runtimes:
  simple: [claude-code, codex]
  medium: [claude-code, codex]
  heavy:  [claude-code, codex]
# Aliases used by the native runtimes (ollama/openai/browser) and the hub
# (router, memory) — only when an Ollama/API provider is configured; with
# subscriptions only they are unused. CHANGE_ME = let Nexus pick from the models the provider
# lists (aliases_auto). The model names below are suggestions for a Mac with
# 16–32 GB of RAM; pull them with `ollama pull <name>`.
aliases:
  fast-local: { provider: ollama, model: "qwen2.5:7b" }          # or "llama3.2:3b" on 8–16 GB Macs; router, tagging, librarian
  embed:      { provider: ollama, model: "nomic-embed-text" }  # keep pinned once memory is indexed
  smart:      { provider: ollama, model: "CHANGE_ME" }         # native-loop fallback for medium work (e.g. "qwen2.5:14b")
  best:       { provider: ollama, model: "CHANGE_ME" }         # e.g. "qwen2.5:32b" with 48 GB+
  # Optional API-key providers (not needed with subscriptions; configure
  # llm.providers.cloud in nexus.yaml / node.yaml first):
  # smart:
  #   provider: cloud
  #   model: "claude-sonnet-4-5"
  #   fallback: [{ provider: ollama, model: "qwen2.5:14b" }]
  # best: { provider: cloud, model: "claude-opus-4-1" }
aliases_auto: true
# Informational: how the aliases map on the subscription CLIs (runtime.claudecode.models /
# runtime.codex.models in node.yaml override these built-in defaults):
cli_models:
  claude-code: { fast-local: haiku, smart: sonnet, best: opus }
  codex:       { smart: "", best: "" }    # "" = the Codex CLI's default model for your plan
tiers:
  simple: [fast-local, smart]
  medium: [smart, best]
  heavy:  [best]
router:
  classifier: fast-local
  fallback: smart
  min_confidence: 0.75
---

Agents reference aliases and runtimes, never provider model names or API keys.

**Subscriptions.** Claude Pro/Max and ChatGPT Plus/Pro are used only through the official CLIs
on a node: Claude Code (`claude`, log in once by running `claude`; for an always-on/headless node
use `claude setup-token` and set `CLAUDE_CODE_OAUTH_TOKEN`) and Codex (`codex login`, "Sign in
with ChatGPT"). Nexus drives the CLIs; it never extracts their tokens. Agents with
`runtime: auto` run on the first available runtime of their tier's list above; Claude Code and
Codex get the agent's Nexus tools (memory, vault, web.*, browser, ask_user, approvals) over the
run's Nexus MCP endpoint. Per-agent CLI settings go in the agent note (`claude:` / `codex:`
blocks, see docs/AGENTS.md).

**Local models.** Ollama handles the cheap work (router classification, tagging, the librarian,
embeddings) and is the last fallback of medium work. Providers are configured in `nexus.yaml`
(`llm.providers`) on the hub and `node.yaml` (`node.llm.providers`) on nodes. With
`aliases_auto: true`, aliases left as `CHANGE_ME` are filled from the models the providers list:
`smart` prefers names containing "sonnet"/"gpt-4", `best` "opus"/"gpt-5", `fast-local`
"haiku"/"mini"/"llama"/"qwen", `embed` "embed". Run `nexusd llm models` to see what was picked and
`nexusd llm check` to probe every alias (chat, tool calling, JSON mode, streaming, embeddings).

Keep `embed` pinned once memory has been indexed: vectors from different embedding models are not
comparable, so `embed` never falls back to a different model.
