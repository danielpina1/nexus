---
type: agent
id: anthrex-dev
name: Talos · Anthrex developer
description: Delivers a coding goal in one of your repositories through Anthrex — plans it into
  tasks, runs Claude Code / Codex workers in git worktrees, checks, reviews and merges them into a
  run branch, then asks you before merging into the base branch. Use for multi-step features,
  refactors and fixes that deserve a plan and review.
icon: hammer.circle
runtime: anthrex
tier: heavy
node: { tags: [dev] }
tools: []
limits: { max_runtime: 8h }
policy:
  compute: approve        # Anthrex plan / hold gates (the runtime asks at approve level anyway)
  code_push: approve      # final merge into the base branch; set biometric for Face ID on every merge
  destructive: biometric  # discarding a run (worktrees and branches)
anthrex:
  # repo: ~/src/myrepo    # default repository (must be in the node's anthrex.allowed_repos);
  #                       # a task's params.repo_dir overrides it
  delivery: local         # local = merge with your approval; pr = stacked pull requests
  design: off             # off = plan directly; full = brainstorm → spec → plan, each approved by you
  # orchestrator: claude  # claude | codex[:model]; default = the node's anthrex.default_orchestrator
version: 1
---

# Anthrex developer

The task prompt is the goal handed to Anthrex verbatim: state the outcome, constraints and how
to verify it (tests to pass, files not to touch). Nexus mirrors the run: each Anthrex task appears
as a step with its state and token usage; Anthrex's decisions come to your inbox — the plan
(approve/reject), work added after the plan (holds), the orchestrator's questions, and finally the
merge into the base branch (a code-push approval showing the branch, diff size and checks).
REPORT.md becomes the task summary and the episode note.

## Lessons learned
