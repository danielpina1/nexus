---
type: workflow
id: price-compare
name: Price compare
description: Compare price, delivery and stock for one product across several stores in parallel,
  summarise a table, and optionally buy at the chosen store.
enabled: true
triggers:
  - intent: ["compare prices", "best price for", "comparar preços", "melhor preço", "onde é mais barato"]
inputs:
  item: { type: text, required: true, description: "Exact product (brand + model) if possible" }
  category: { type: text, required: false, description: "Store category (electronics, books, home, sports, groceries…)" }
  stores: { type: text, required: false, description: "Comma-separated store ids; empty = by category" }
  budget: { type: text, required: false }
steps:
  - id: stores
    tool: shops.list
    with: { category: "{{ inputs.category }}", ids: "{{ inputs.stores }}", buyable: true, limit: 4, query: "{{ inputs.item }}" }
  - id: gather
    parallel:
      - id: research
        foreach: "steps.stores.output.shops"
        as: store
        parallel: 4
        do:
          - id: check
            agent: shop
            on_error: continue
            task: |
              RESEARCH MODE — do not add anything to the cart and do not check out.
              Store: {{ store.id }} ({{ store.name }}). Call shops.get {"id": "{{ store.id }}", "query": "{{ inputs.item }}"} first.
              Product: {{ inputs.item }}
              Find the matching product on this store only and answer with the findings block
              (price, delivery cost and date, total, stock, seller, product URL). If it is not
              sold there, say "not found" and the closest alternative.
      - id: kk
        agent: shop
        on_error: continue
        task: |
          RESEARCH MODE — do not buy. Use the price-comparison store kuantokusta (shops.get it first).
          Product: {{ inputs.item }}
          List the 5 cheapest offers (store, price, shipping, availability) as findings lines.
  - id: table
    agent: assistant
    task: |
      Compare offers for: {{ inputs.item }} (budget: {{ inputs.budget != '' ? inputs.budget : 'none' }}).
      Store research: {{ toJson(steps.research.output) }}
      Price-comparison site: {{ steps.kk.output }}
      Write a short Markdown table (store · price · delivery · total · delivery date · stock · seller),
      cheapest total first, only offers for the same product (flag different variants), then one
      line with the recommendation. Mark prices seen only on the comparison site as unconfirmed.
  - id: send
    tool: notify
    with: { title: "Prices: {{ inputs.item }}", body: "{{ steps.table.output }}" }
  - id: choice
    ask:
      question: "Buy {{ inputs.item }}? Reply with the store (e.g. worten) or pick an option."
      choices: ["cheapest", "no"]
      allow_free_text: true
      default: "no"
  - id: buy
    if: "steps.choice.output != 'no' && steps.choice.output != ''"
    agent: shop
    task: |
      Buy: {{ inputs.item }}
      Store: {{ steps.choice.output == 'cheapest' ? 'the cheapest suitable offer from the comparison below' : steps.choice.output }}
      The user already chose — do not ask which store again; still confirm variant/price changes.
      Comparison: {{ steps.table.output }}
---

Fan-out price research: one [[Shopper]] run per store in research mode (no cart), plus the
KuantoKusta comparison site, then the assistant writes the table and Nexus asks whether to buy.
The buy step is a normal shop run: checkout still needs the biometric approval.

Stores come from `shops.list` (vault `shops/` notes, `buyable` ones, max 4, by category or the ids
given). Add a store by adding a note — see docs/SHOPPING.md.
