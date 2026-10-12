---
type: workflow
id: weekly-review
name: Weekly review
description: Sunday evening summary of the week's episodes, saved to the daily note and sent as a brief.
enabled: true
triggers:
  - cron: "0 18 * * 0"
    tz: Europe/Lisbon
  - intent: ["weekly review", "review my week", "revisão da semana"]
steps:
  - id: review
    agent: assistant
    task: |
      Weekly review for the 7 days ending {{ string(now).substring(0, 10) }}.
      Use memory.search and vault.read on episodes/ (and the daily notes) from the last 7 days.
      Summarize in Markdown, max 15 bullets, under these headings:
      - Done (what got finished, by which agent)
      - Failed or stuck (with the reason)
      - Open loops (pending approvals, questions, unfinished tasks)
      - Suggestions for next week (recurring chores worth a workflow, lessons for agents)
  - id: save
    tool: vault.daily_append
    with: { text: "## Weekly review\n\n{{ steps.review.output }}" }
  - id: send
    tool: notify
    with: { title: "Weekly review", body: "{{ steps.review.output }}", kind: brief }
---

Runs Sundays at 18:00 Lisbon time. The review lands in that day's daily note so it is searchable
later; the notice is the phone-friendly copy.
