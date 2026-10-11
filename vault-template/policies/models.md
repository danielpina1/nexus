---
type: policy
id: models
name: Model aliases
aliases:
  fast-local: { provider: ollama, model: "CHANGE_ME", node_tags: [gpu] }
  embed:      { provider: ollama, model: "CHANGE_ME", node_tags: [gpu] }
  smart:      { provider: unify,  model: "CHANGE_ME" }
  best:       { provider: unify,  model: "CHANGE_ME" }
tiers:
  simple: [fast-local, smart]
  medium: [smart, best]
  heavy:  [claude-code, codex]
router:
  classifier: fast-local
  fallback: smart
  min_confidence: 0.75
---

Agents reference these aliases, never provider model names. Fill in the gateway and local model
names once Unify Gateway and Ollama are set up.
