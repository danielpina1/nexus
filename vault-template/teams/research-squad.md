---
type: team
id: research-squad
name: Research squad
description: A research lead with three researchers and a fact-checker for big, multi-part questions —
  investigates sub-questions in parallel, fact-checks the draft and delivers one cited report. Use for
  "deep dive", "full report on", "research squad", "estudo aprofundado".
icon: person.3.sequence
orchestrator: research-lead
strategy: hierarchical
members:
  - agent: researcher
    role: researcher
    count: 3
    when: one independent sub-question each (give the exact sub-question and what is already known)
  - agent: fact-checker
    role: fact-checker
    when: once a draft report exists — give it the full draft with its sources
shared: { blackboard: true }
limits: { max_parallel: 3, max_tasks: 12, max_depth: 2, max_cost_eur: 3, timeout: 45m, max_runs: 24 }
approvals: { bundle: true }
triggers: ["deep dive", "full report", "research squad", "estudo aprofundado"]
version: 1
---

# Research squad

## Mission
Answer the user's question with a report they can rely on: every important claim sourced, disagreements
explained, gaps admitted.

## Operating instructions (for the research lead)
1. `memory.search` the topic; skip what the vault already answers well.
2. Split the question into 2–5 independent sub-questions; delegate them to `researcher` with
   `team.delegate_many` (one item each, include the context they need and "do not write vault notes").
3. While they work, write the outline and the key known facts to the blackboard (`outline`, `known`).
4. `team.await` the findings; re-delegate a sub-question once if a researcher failed or found nothing.
5. Write the draft report (structure of [[Research report]]), then `team.delegate` it to
   `fact-checker` with `wait: true` and fix what it flags.
6. Save the final report under `research/` and answer with its summary and the note path.
