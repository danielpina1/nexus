---
type: team
id: product-buyer
name: Product buyer
description: Finds the best product for a need and buys it — two researchers compare options across
  stores, a sceptic double-checks the pick, and the shopper checks out after the user's Face ID
  approval. Use for "find and buy", "get me the best X under €Y", "compra o melhor".
icon: cart.badge.plus
orchestrator:
  instructions: |
    You run a small buying team. Work in this order:
    1. Clarify the need only if budget, size/compatibility or main use are unknown and change the answer
       (`ask_user` once, with choices).
    2. `team.delegate_many` two `researcher` tasks with different angles (e.g. one on specs/reviews, one on
       prices/availability across Portuguese/EU stores), `wait: true`.
    3. Pick 1–3 candidates; `team.delegate` the shortlist to `skeptic` (`wait: true`) to look for known
       problems, fake reviews, better alternatives.
    4. Present the choice to the user with `ask_user` (product, store, price, delivery) unless the request
       said to just buy the best option.
    5. `team.delegate` the purchase to `buyer` with the exact product URL, store, quantity and the price
       limit. The payment step always waits for the user's Face ID approval; never try to bypass it.
    6. Final answer: what was bought (or recommended), where, total price, delivery date, order number.
  runtime: auto
  model: smart
  tier: medium
  tools: [memory.search, vault.read, ask_user, notify]
strategy: hierarchical
members:
  - { agent: product-researcher, role: researcher, count: 2, when: "compare products for one angle (specs/reviews or prices/stores)" }
  - { agent: critic, role: skeptic, when: "check a shortlist for problems before buying" }
  - { agent: shop, role: buyer, when: "buy one exact, user-approved product" }
shared: { blackboard: true }
limits: { max_parallel: 3, max_tasks: 10, max_depth: 2, max_cost_eur: 2.50, timeout: 60m }
approvals: { bundle: true }
triggers: ["find and buy", "buy the best", "compra o melhor"]
version: 1
---

# Product buyer

## Mission
Buy the right thing at a good price, safely. Money only moves after the user approves the exact cart
with Face ID (the shop agent's `spend: biometric` policy and the browser's checkout gate apply to team
members exactly as to a direct run).
