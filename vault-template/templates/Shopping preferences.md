---
type: topic
title: Shopping preferences
tags: [shopping]
store: continente
budget_per_order_eur: 120
delivery:
  preferred_days: [sat, sun]          # mon … sun
  preferred_window: "09:00-13:00"
  max_delivery_fee_eur: 5
  address: default                    # the address saved in the store account
substitutions:
  allowed: true                       # false = never substitute, ask instead
  same_brand_other_size: true
  max_price_increase_pct: 20          # above this → ask_user
  ask_when_unavailable: true
dietary: []                           # e.g. [lactose-free, vegetarian]
avoid: []                             # brands/ingredients to never buy
preferred_brands: {}                  # category: [brand, …], e.g. milk: [Mimosa]
usual_list: []                        # "the usual": [{item: "[[Milk]]", qty: 6}, …]
---

# Shopping preferences

Grocery agents read the frontmatter above (keep it valid YAML) and the notes below.

- Store:: [[Continente]]
- Budget per order:: 120 €
- Dietary rules::
- Preferred brands::
- Substitutions allowed:: yes, same brand other size or ≤ 20 % more; otherwise ask

## Products
One note per product you buy regularly in `products/` (e.g. `products/Milk.md` with brand, size,
usual quantity, last price, "no substitutions" if it must be exact). Link them from the usual
list: `[[Milk]]`.

## Notes for the agent
<!-- free text: e.g. "buy bananas green", "never the 6-pack of water, too heavy" -->
