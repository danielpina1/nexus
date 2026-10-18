---
type: agent
id: anthrex-design
name: Anthrex designer-developer
description: Like anthrex-dev, but for larger or unclear goals — Anthrex first brainstorms
  approaches, writes a spec and a plan, and you approve each document before any code is
  written. Use when the goal needs design decisions, not only implementation.
icon: pencil.and.ruler
runtime: anthrex
tier: heavy
node: { tags: [dev] }
tools: []
limits: { max_runtime: 12h }
policy:
  compute: approve
  code_push: approve      # set biometric for Face ID on every merge
  destructive: biometric
anthrex:
  # repo: ~/src/myrepo
  delivery: local
  design: full            # brainstorm, spec and plan gates — each an approval in your inbox
version: 1
---

# Anthrex designer-developer

Each design document (brainstorm report, spec, plan) arrives as an approval with the document
text. Approve to move on; deny to answer what should change (your note is sent to Anthrex's
orchestrator, or pick "back"/"rethink"/"reject"). The rest is as for anthrex-dev.

## Lessons learned
