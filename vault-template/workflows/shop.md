---
type: workflow
id: shop
name: Shop
description: Buy something online — the shop agent compares stores when none is named, asks, and
  checks out after a biometric approval.
enabled: true
triggers:
  - intent: ["buy", "comprar", "order", "encomendar", "quero comprar", "buy me"]
  - share: { types: [url, text], hint: product }
inputs:
  item: { type: text, required: true, description: "What to buy (product, model, variant, quantity)" }
  budget: { type: text, required: false, description: "Max price, e.g. 150 €" }
  store: { type: text, required: false, description: "Store id or name (worten, fnac, amazon-es…); empty = compare" }
  cheapest: { type: bool, required: false, default: false, description: "Buy the cheapest match without asking which store" }
steps:
  - id: buy
    agent: shop
    task: |
      Buy: {{ inputs.item }}
      Budget: {{ inputs.budget != '' ? inputs.budget : 'none given (respect [[Shopping preferences]] and max_spend_eur)' }}
      Store: {{ inputs.store != '' ? inputs.store : 'not specified: compare 2-4 suitable stores (shops.list) and ask me which one' }}
      {{ inputs.cheapest == true ? 'Just buy the cheapest suitable offer (best total incl. delivery) without asking which store.' : '' }}
---

Thin wrapper so the command bar, Siri and the share sheet (a product link or name) can start the
[[Shopper]] agent with structured inputs. Stores are the notes in `shops/`; see docs/SHOPPING.md.

Checkout always pauses for a biometric approval showing the store, items, total and a screenshot.
