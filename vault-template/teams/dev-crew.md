---
type: team
id: dev-crew
name: Dev crew
description: An architect plans a code change, two coders implement the steps (Claude Code / Codex), a
  tester verifies and a reviewer checks the diff before anything is pushed. Use for "implement this
  feature", "dev crew", "fix and review", "refactor with tests".
icon: hammer.circle
orchestrator: architect
strategy: hierarchical
members:
  - { agent: code-fixer, role: coder, count: 2, when: "implement one planned step in the repository (give repo path, branch, step, test command)" }
  - { agent: tester, role: tester, when: "run the tests / add a failing test for a step before or after coding" }
  - { agent: reviewer, role: reviewer, when: "review the final diff; blocking findings go back to a coder" }
  - { agent: anthrex-dev, role: delivery, when: "a larger multi-step coding goal: Anthrex plans it, runs workers in worktrees, reviews and merges (asks before merging into the base branch)" }
shared: { blackboard: true }
limits: { max_parallel: 2, max_tasks: 14, max_depth: 2, max_cost_eur: 12, timeout: 3h, max_runs: 30 }
approvals: { bundle: true }
triggers: ["dev crew", "implement this feature", "fix and review"]
version: 1
---

# Dev crew

## Mission
Ship a correct, reviewed change in small steps. Pipeline per step: plan → (tester: failing test) →
coder → tester → reviewer. Independent steps may run in parallel (two coders), dependent ones in order.
Write the plan and the branch name to the blackboard (`plan`, `branch`) so every member works on the
same branch. Coding tools (`anthrex.*`) belong to coding members only (code-fixer, tester, anthrex-dev);
hand whole multi-step goals to `delivery` (Anthrex) instead of splitting them yourself. Pushing a branch or opening a PR always asks the user (code_push: approve).
