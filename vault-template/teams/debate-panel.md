---
type: team
id: debate-panel
name: The Areopagus · Debate panel
description: Three analysts with different perspectives argue a decision over two rounds and a judge
  gives a reasoned verdict. Use for hard decisions and contested questions — "should I", "debate",
  "pros and cons of", "devo ou não".
icon: bubble.left.and.bubble.right
strategy: debate
debate: { rounds: 2 }
members:
  - { agent: analyst, role: optimist, when: "make the strongest case FOR (benefits, upside, opportunity)" }
  - { agent: analyst, role: sceptic, when: "make the strongest case AGAINST (costs, risks, failure modes)" }
  - { agent: analyst, role: pragmatist, when: "focus on what is practical for the user: constraints, alternatives, timing" }
  - { agent: judge, role: judge }
limits: { max_parallel: 3, max_tasks: 10, max_cost_eur: 1.50, timeout: 30m }
triggers: ["debate", "pros and cons", "devo ou não"]
version: 1
---

# Debate panel

## Mission
Help the user decide. The hub runs the debate (round 1: positions; round 2: rebuttals seeing the others),
then the judge weighs the final positions. The judge's verdict is the result.
