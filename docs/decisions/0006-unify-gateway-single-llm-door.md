# ADR-0006: Unify Gateway as the single door to cloud models

- Status: proposed (API details pending)
- Date: 2026-10-11

## Context
Agents use many models. Keys, model choice and cost tracking should not be scattered across agents.

## Decision
All cloud LLM calls from Nexus-native agents, the router and the memory pipeline go through the
Unify Gateway. Agents reference model *aliases* (`policies/models.md`), never provider ids. Local
models (Ollama) use the same OpenAI-compatible client. Claude Code and Codex are runtimes with their
own model access and are configured per Node.

## Consequences
- Swapping models is a config change; cost data comes from one place.
- The gateway is a dependency: the router has a local-only fallback so basic commands work when the
  gateway is unreachable.
