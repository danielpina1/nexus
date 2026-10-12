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
    task: |
      Build the usual grocery list: start from usual_list in [[Shopping preferences]], then adjust
      with the last 4 orders in episodes/shopping/. One item per line with quantity.
  - id: shop
    agent: grocery-continente
    task: |
      List: {{ inputs.list != '' ? inputs.list : steps.usual.output }}
      Guidelines: {{ inputs.guidelines }}
      Follow [[Shopping preferences]] for brands, budget, delivery slot and substitutions.
---

Thin wrapper so the share sheet and Siri can start a grocery run with or without a list.

The `grocery-continente` agent runs on a node tagged `browser` with a persistent, logged-in
Continente profile (log in once — see the agent note). Checkout always pauses for a biometric
approval showing a screenshot of the final cart.
