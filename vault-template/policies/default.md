---
type: policy
id: default
name: Default trust levels
levels:
  read: auto
  write_internal: auto
  compute: auto
  write_external: approve
  code_push: approve
  spend: biometric
  destructive: biometric
  device_control: notify
budgets:
  llm_per_day_eur: 5
  spend_per_day_eur: 200
approvals:
  expire_after: 2h
  earned_autonomy:
    enabled: true
    suggest_after_approvals: 10
agent_note_changes_by_agents: approve
---

Global policy. Agents may tighten these levels but never loosen them.
