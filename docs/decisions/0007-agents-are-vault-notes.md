# ADR-0007: Agents, workflows and policies are vault notes

- Status: accepted
- Date: 2026-10-11

## Context
The user wants "agents for everything" and to configure what an agent is without writing code.

## Decision
An agent is a Markdown note (frontmatter = config, body = instructions/playbook) bound to a runtime
adapter. Workflows and policies follow the same pattern. The Hub watches these folders and reloads
on change; invalid notes are reported, never half-loaded.

## Consequences
- New agents can be created from Obsidian, the dashboard, the phone or by another agent (with approval).
- Schema validation and clear error cards are required.
- Changes to agents/policies by agents are `approve` level by default.
