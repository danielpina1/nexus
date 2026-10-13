---
type: workflow
id: server-watchdog
name: Server watchdog
description: When a node goes offline (or its disk fills up), the ops agent investigates and reports.
enabled: false   # turn on once you have an always-on machine; on a laptop it fires on every wake-up
triggers:
  - event: node.offline
    inputs: { node: "{{ event.node_status.node_id }}", alert: "node offline" }
  - event: node.disk_high
    inputs: { node: "{{ event.node_status.node_id }}", alert: "disk above 90%" }
  - intent: ["check the servers", "server watchdog", "are my machines ok"]
inputs:
  node: { type: text, required: false, description: "Node id to look at; empty = all nodes" }
  alert: { type: text, required: false, description: "What fired the watchdog" }
steps:
  - id: status
    tool: nexus.status
  - id: investigate
    agent: ops
    task: |
      {{ inputs.alert != '' ? 'Alert: ' + inputs.alert + ' on node ' + inputs.node + '.' : 'Routine health check of all nodes.' }}
      Current Nexus status: {{ steps.status.output }}

      Diagnose the cause (reachability, last telemetry, recent failed runs, logs on the other
      nodes if useful). Safe fixes such as restarting a service are allowed; anything destructive
      or that changes configuration must go through request_approval. Finish with a short report:
      what happened, what you did, what still needs a human.
  - id: notify
    tool: notify
    with:
      title: "{{ inputs.alert != '' ? 'Watchdog: ' + inputs.node + ' — ' + inputs.alert : 'Watchdog report' }}"
      body: "{{ steps.investigate.output }}"
      kind: alert
---

Fires on the `node.offline` and `node.disk_high` event topics (de-duplicated per transition by
the event trigger source) or on demand from the command bar. Destructive fixes stay behind the
ops agent's own approval policy.
