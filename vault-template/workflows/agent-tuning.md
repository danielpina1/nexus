---
type: workflow
id: agent-tuning
name: Weekly agent review
description: Sunday review of agent outcomes; drafts improvements for agents that fail or keep being corrected. Nothing is published without your approval.
enabled: true
triggers:
  - cron: "40 17 * * 0"
    tz: Europe/Lisbon
  - intent: ["review my agents", "tune my agents", "rever agentes"]
steps:
  - id: tune
    tool: architect.tune
    with: { days: 7, max_drafts: 3 }
  - id: send
    tool: notify
    with: { title: "Agent review", body: "{{ steps.tune.output.summary }}", kind: suggestion }
---

Every Sunday the Agent Architect looks at the last 7 days: agents whose tasks failed at least 30% of
the time (minimum 3 tasks), or whose routing was corrected twice or more, get an improvement draft
(`architect.tune` → Draft with `update_agent_id`). Each draft carries the evidence (failed task
summaries, where the owner re-routed the tasks) and still has to be reviewed, sandbox-tested and
approved in **Agents → Architect** before anything changes. See docs/ARCHITECT.md.
