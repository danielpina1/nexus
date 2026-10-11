---
type: workflow
id: groceries
name: Groceries
enabled: true
triggers:
  - intent: ["buy groceries", "lista de compras", "buy the usual"]
  - share: { types: [text, image], hint: shopping-list }
inputs:
  list: { type: text, required: false, description: "Items; empty = the usual list" }
  guidelines: { type: text, required: false }
steps:
  - id: usual
    if: "inputs.list == ''"
    agent: assistant
    task: "Build the usual grocery list from the last 4 orders in episodes/shopping/."
  - id: shop
    agent: grocery-continente
    task: |
      List: {{ inputs.list != '' ? inputs.list : steps.usual.output }}
      Guidelines: {{ inputs.guidelines }}
---

Thin wrapper so the share sheet and Siri can start a grocery run with or without a list.
