---
type: workflow
id: quick-answer
name: Quick answer
description: One researcher looks something up on the web and answers briefly with citations;
  the Q&A is logged in today's daily note.
enabled: true
triggers:
  - intent: ["quick answer", "quick question", "look up", "search the web for", "search the web",
      "pesquisa rápida", "procura na internet", "procura na web"]
inputs:
  question:
    type: text
    required: true
    description: The question to answer.
steps:
  - id: answer
    agent: researcher
    model: smart
    timeout: 10m
    task: |
      QUICK ANSWER — do not write vault notes.
      Question: {{ inputs.question }}
      Today: {{ string(now).substring(0, 10) }}

      Check memory.search first. Then web.search (count 5–8) and read 2–4 good sources with
      web.extract. Answer in at most 150 words in the question's language, with inline
      citations [n], followed by the numbered sources (title — site, date, URL). Say clearly when
      sources disagree or something could not be verified.
  - id: log
    if: "!run.dry_run"
    tool: vault.daily_append
    on_error: continue
    with: { text: "### Q: {{ inputs.question }}\n\n{{ steps.answer.output }}" }
---

# Quick answer

The answer reaches you as the agent task's result in the Inbox; the `log` step keeps the Q&A in
the daily note so later searches find it. For anything that needs more than a few sources, use
[[deep-research]].
