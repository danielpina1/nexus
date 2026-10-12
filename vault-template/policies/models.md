---
type: policy
id: models
name: Model aliases
# CHANGE_ME = let Nexus pick from the models the provider lists (aliases_auto).
# Explicit model names always win. Fallbacks are tried in order when the
# primary provider fails or is cooling down.
aliases:
  fast-local: { provider: ollama, model: "CHANGE_ME", node_tags: [gpu] }
  embed:      { provider: ollama, model: "CHANGE_ME", node_tags: [gpu] }
  smart:      { provider: ollama, model: "CHANGE_ME" }
  best:       { provider: ollama, model: "CHANGE_ME" }
  # With a cloud provider configured (llm.providers.cloud):
  # smart:
  #   provider: cloud
  #   model: "claude-sonnet-4-5"
  #   fallback: [{ provider: ollama, model: "qwen2.5:14b" }]
aliases_auto: true
tiers:
  simple: [fast-local, smart]
  medium: [smart, best]
  heavy:  [claude-code, codex]
router:
  classifier: fast-local
  fallback: smart
  min_confidence: 0.75
---

Agents reference these aliases, never provider model names. Providers are configured in
`nexus.yaml` (`llm.providers`) on the hub and `node.yaml` (`node.llm.providers`) on nodes: local
Ollama by default, optionally a cloud API or any OpenAI- or Anthropic-compatible gateway.

With `aliases_auto: true`, aliases left as `CHANGE_ME` are filled from the models the providers
list: `smart` prefers names containing "sonnet"/"gpt-4", `best` "opus"/"gpt-5", `fast-local`
"haiku"/"mini"/"llama"/"qwen", `embed` "embed". Run `nexusd llm models` to see what was picked and
`nexusd llm check` to probe every alias (chat, tool calling, JSON mode, streaming, embeddings).

Keep `embed` pinned once memory has been indexed: vectors from different embedding models are not
comparable, so `embed` never falls back to a different model.
