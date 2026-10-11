---
type: workflow
id: morning-brief
name: Morning brief
enabled: false
triggers:
  - cron: "30 7 * * 1-5"
    tz: Europe/Lisbon
  - intent: ["morning brief", "what's my day", "resumo do dia"]
steps:
  - id: ops
    tool: nexus.status
  - id: cal
    agent: assistant
    task: "List today's calendar events and conflicts."
  - id: brief
    agent: assistant
    model: fast-local
    task: |
      Write a short morning brief (max 8 bullets) from:
      calendar: {{ steps.cal.output }}
      system: {{ steps.ops.output }}
  - id: save
    tool: vault.daily_append
    with: { text: "{{ steps.brief.output }}" }
  - id: send
    tool: notify
    with: { title: "Good morning", body: "{{ steps.brief.output }}", card: brief }
---

Daily brief at 07:30 on weekdays. Extend with email and weather once those integrations exist.
