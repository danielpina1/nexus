# ADR-0003: Markdown vault (git) as the source of truth for knowledge and configuration

- Status: accepted
- Date: 2026-10-11

## Context
The knowledge base should feel like Obsidian, be readable without Nexus, versioned, and editable by
humans and agents alike. Agent, workflow and policy definitions should be just as easy to edit.

## Decision
Store knowledge, agents, workflows and policies as Markdown files with YAML frontmatter in a git
repository. All databases over it (graph, FTS, vectors) are derived indexes that can be rebuilt.

## Consequences
- Open the vault in Obsidian; no lock-in; full history; easy backup.
- Every agent edit is a commit → auditable and revertible.
- Need an indexer with incremental updates and a merge/conflict strategy for concurrent edits.
- Graph queries are limited to what the index supports; a dedicated graph DB can be added later as
  another derived index if needed.
