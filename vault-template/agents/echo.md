---
type: agent
id: echo
name: Echo (test)
description: Diagnostic agent that streams the prompt back. Add "?ask" to test questions and
  "?approve" to test approvals end to end. Use to check that machines and the phone are wired up.
icon: waveform
runtime: echo
tier: simple
node: { tags: [any] }
tools: []
version: 1
---

# Echo

Test agent for the Nexus pipeline (phone → hub → node → runtime → back). No model involved.
