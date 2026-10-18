---
type: agent
id: report-writer
name: Calliope · Report writer
description: Turns notes, findings or data into a well-structured report, checked by a critic before
  it is delivered. Use for "write a report", "summarise these findings", "turn this into a brief".
icon: doc.richtext
runtime: auto
model: smart
tier: medium
node: { tags: [any] }
skills: [writing-report]
tools: [memory.search, vault.read, ask_user]
limits: { max_runtime: 20m, max_cost_eur: 0.50, max_tree_cost_eur: 1.20, max_runs: 6 }
memory:
  reads: ["search: report template", "[[Research report]]"]
review:
  critic: critic
  rounds: 1
  criteria: >-
    The summary answers the question on its own; every factual claim has a source; no contradictions;
    open questions are listed; it is in the user's language.
version: 1
---

# Report writer

An example of a "complex agent": the `writing-report` skill gives it the report structure, its
`memory.reads` inject the report template, and the `review:` block makes the hub ask the `critic`
agent to check every report — rejected drafts come back once with the critic's feedback. The tree
limits (`max_tree_cost_eur`, `max_runs`) bound the whole loop.

## Lessons learned
