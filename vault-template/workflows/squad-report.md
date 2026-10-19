---
type: workflow
id: squad-report
name: Squad report
description: Hands a big question to the research-squad team, waits for its cited report, saves it
  under research/ and sends you the summary. Weekly or on demand.
enabled: false
triggers:
  - intent: ["squad report", "team report on", "relatório da equipa"]
inputs:
  question:
    type: text
    required: true
    description: The question for the research squad (add context and constraints).
steps:
  # 1. A team step starts a whole team run (orchestrator + members, the team's own limits) and waits
  #    until it ends. Its output is the team's result: the orchestrator's final answer.
  - id: squad
    team: research-squad
    timeout: 50m
    prompt: |
      Full report: {{ inputs.question }}
      Today: {{ string(now).substring(0, 10) }}
      Do NOT write vault notes yourself; the workflow saves the report. Answer with the full report.
    inputs:
      question: "{{ inputs.question }}"
      requested_by: "workflow:squad-report"

  # 2. Save it (the team run's task id and cost are in steps.squad.result).
  - id: save
    if: "!run.dry_run"
    tool: vault.write
    with:
      path: "research/{{ string(now).substring(0, 10) }}-squad-{{ run.id.lowerAscii().replace('_', '-') }}.md"
      message: "squad-report: {{ inputs.question }}"
      content: |
        ---
        type: research
        title: {{ toJson(inputs.question) }}
        created: {{ string(now) }}
        source: "workflow:squad-report"
        team_run: {{ steps.squad.result.task_id }}
        tags: [research]
        ---

        {{ steps.squad.output }}

  - id: send
    if: "!run.dry_run"
    tool: notify
    with:
      title: "Squad report: {{ inputs.question }}"
      body: |
        {{ steps.squad.output.split('\n## ')[0].trim() }}

        Full report: [[{{ steps.save.output.path }}]]
      kind: result
---

# Squad report

Example of a **team step**: `team: <id>` starts a team run (see docs/SWARMS.md) with the rendered
`prompt` (or `task`) and `inputs` (passed as the run's params), waits for it, and exposes the
team's final answer as `steps.<id>.output` (`steps.<id>.result` holds the structured result,
`task_id`, `team_id` and the run's cost). The team's limits (`max_cost_eur`, `timeout`, …) apply as
usual; the step `timeout` cancels the team run when it is exceeded. A failed team run fails the
step (use `on_error: continue` to go on).

Disabled in the template: enable it once `research-squad` and its agents are set up.
