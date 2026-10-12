---
type: workflow
id: deep-research
name: Deep research
description: Splits a big question into sub-questions, researches them in parallel on the web,
  synthesises a cited report linked to your notes, fact-checks it, saves it under research/ and
  sends you the summary.
enabled: true
triggers:
  - intent: ["research", "research on", "research about", "deep research", "deep dive", "deep dive into",
      "research report", "pesquisa", "pesquisa sobre", "pesquisa aprofundada", "investigação sobre"]
inputs:
  question:
    type: text
    required: true
    description: What to research (one question or topic; add context and constraints).
  depth:
    type: text
    default: standard
    choices: [quick, standard, deep]
    description: "quick: 2–3 sub-questions, no fact-check · standard: 3–4 · deep: 5–6, more sources"
steps:
  # 1. research-lead plans: TITLE / SLUG / Q: lines (see agents/research-lead.md, plan mode).
  - id: plan
    agent: research-lead
    model: smart
    timeout: 10m
    task: |
      PLAN
      Question: {{ inputs.question }}
      Depth: {{ inputs.depth }}
      Today: {{ string(now).substring(0, 10) }}

  # 2. One researcher per sub-question, 4 at a time. A failed sub-question does not fail the
  #    report (on_error: continue); with no parsable Q: lines the whole question is researched.
  - id: research
    foreach: >-
      size(steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('Q:'))) > 0
      ? steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('Q:')).map(l, l.substring(2).trim())
      : [inputs.question]
    as: q
    parallel: 4
    do:
      - id: investigate
        agent: researcher
        on_error: continue
        retries: 1
        timeout: 25m
        task: |
          Research this sub-question. It is part of a larger report, so: do NOT write vault notes
          and do NOT ask the user questions (state your assumptions instead).

          Sub-question: {{ q }}
          Overall question: {{ inputs.question }}
          Depth: {{ inputs.depth }} (quick: 3–5 sources · standard: 5–8 · deep: 8–15)
          Today: {{ string(now).substring(0, 10) }}

          Return findings in your playbook format: "### <sub-question>", a direct answer, key
          findings with [n] citations, uncertainties, and "#### Sources".

  # 3. research-lead merges the findings into one report (synthesis mode).
  - id: synth
    agent: research-lead
    timeout: 20m
    task: |
      SYNTHESISE
      Question: {{ inputs.question }}
      Depth: {{ inputs.depth }}
      Today: {{ string(now).substring(0, 10) }}

      Plan:
      {{ steps.plan.output }}

      Findings from the researchers (each block starts with its sub-question; citation numbers are
      per block — merge the sources into one list and renumber):

      {{ steps.research.result.items.map(i, i.investigate.output).join('\n\n---\n\n') }}

  # 4. fact-checker reviews the draft (skipped for quick); 5. research-lead fixes issues.
  - id: check
    if: inputs.depth != 'quick'
    agent: fact-checker
    on_error: continue
    timeout: 15m
    task: |
      Fact-check this research report draft. Check the claims the summary and key findings depend
      on against their cited sources.

      {{ steps.synth.output }}

  - id: revise
    if: "steps.check.state == 'succeeded' && steps.check.output.trim().startsWith('VERDICT: issues')"
    agent: research-lead
    on_error: continue
    timeout: 15m
    task: |
      REVISE
      Report:
      {{ steps.synth.output }}

      Fact-check review:
      {{ steps.check.output }}

  # 6. Save research/<date>-<slug>.md (the report = revised draft when there is one).
  - id: save
    if: "!run.dry_run"
    tool: vault.write
    with:
      path: >-
        research/{{ string(now).substring(0, 10) }}-{{ size(steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('SLUG:')).map(l, l.substring(5).trim().lowerAscii()).filter(s, s.matches('^[a-z0-9][a-z0-9-]{1,79}$'))) > 0 ? steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('SLUG:')).map(l, l.substring(5).trim().lowerAscii()).filter(s, s.matches('^[a-z0-9][a-z0-9-]{1,79}$'))[0] : 'report-' + run.id.lowerAscii().replace('_', '-') }}.md
      message: "deep-research: {{ inputs.question }}"
      content: |
        ---
        type: research
        title: {{ toJson(size(steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('TITLE:'))) > 0 ? steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('TITLE:'))[0].substring(6).trim() : inputs.question) }}
        question: {{ toJson(inputs.question) }}
        depth: {{ inputs.depth }}
        created: {{ string(now) }}
        source: "workflow:deep-research"
        workflow_run: {{ run.id }}
        tags: [research]
        ---

        {{ steps.revise.state == 'succeeded' && steps.revise.output != '' ? steps.revise.output : steps.synth.output }}

        {{ steps.check.state == 'succeeded' ? '## Fact-check\n\n' + steps.check.output : '' }}

  # 7. Phone notification with the summary section.
  - id: send
    if: "!run.dry_run"
    tool: notify
    with:
      title: >-
        Research: {{ size(steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('TITLE:'))) > 0 ? steps.plan.output.split('\n').map(l, l.trim()).filter(l, l.startsWith('TITLE:'))[0].substring(6).trim() : inputs.question }}
      body: |
        {{ (steps.revise.state == 'succeeded' && steps.revise.output != '' ? steps.revise.output : steps.synth.output).split('\n## ')[0].replace('## Summary', '').trim() }}

        Full report: [[{{ steps.save.output.path }}]]
      kind: result
---

# Deep research

Fan-out / fan-in research: the `research-lead` agent plans, `researcher` agents investigate
sub-questions in parallel, the `research-lead` writes the report (structure:
[[Research report]]), the `fact-checker` reviews it and the lead fixes what it flagged.

Why a workflow and not one agent: `spawn_agent` cannot wait for a child's answer, so an agent
cannot collect its sub-tasks' results. The workflow's `foreach` does the waiting and hands every
finding to the synthesis step.

- Start it from the command bar ("deep dive into home batteries for a 3-bedroom house") or from
  an agent: `workflow.run {workflow_id: deep-research, inputs: {question, depth}}`.
- `depth: quick` skips the fact-check; `deep` asks for more sub-questions and sources.
- Cost grows with depth: roughly (sub-questions × researcher) + 2–4 research-lead/fact-checker
  calls. Researchers run 4 at a time.
- Each researcher's task is a separate root task, so you may also see their individual results in
  the Inbox.
- Web access needs the `tools.web` module (see docs/RESEARCH.md) on the node that runs the agents.
