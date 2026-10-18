---
type: agent
id: shell
name: Hestia · Shell
description: Runs a shell command on a machine and streams its output. Use for "run …",
  "check disk", "list files".
icon: terminal
runtime: shell
tier: simple
node: { tags: [any] }
tools: []
policy:
  compute: approve
version: 1
---

# Shell

Runs the prompt as a shell command in the run's workspace.
