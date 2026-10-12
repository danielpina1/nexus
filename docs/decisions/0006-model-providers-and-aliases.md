# ADR-0006: Model access through configurable providers and aliases

- Status: accepted (supersedes the earlier "Unify Gateway as LLM door" draft, which was a
  misunderstanding: the user's *UniFi Gateway* is the home network gateway — see ADR-0009)
- Date: 2026-10-12

## Context
Agents use many models (local and cloud). Keys, model choice and cost tracking should not be
scattered across agents.

## Decision
The `llm` module is the single door to models. Providers (local Ollama, cloud APIs, any OpenAI- or
Anthropic-compatible endpoint) are configured on the Hub and on each Node. Agents reference model
*aliases* (`policies/models.md`), never provider ids; aliases can have fallback chains. Claude Code
and Codex are runtimes with their own model access, configured per Node.

## Consequences
- Swapping models is a config change; `nexusd llm check` verifies providers and aliases.
- The router has a local-only fallback so basic commands work when a cloud provider is unreachable.
