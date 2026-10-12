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
# Online shopping at any store (shop agent, price-compare workflow). Store ids = shops/ notes.
stores:                               # preferred stores per category, in order
  groceries: [continente]
  electronics: [worten, fnac, pcdiga]
  books: [fnac]
  home: [ikea]
  sports: [decathlon]
  general: [amazon-es]
avoid_stores: []                      # store ids never to buy from
compare_stores: 3                     # how many stores to compare when none is named
marketplace_sellers: ask              # allow | ask | never ("Vendido por" a third party)
prefer_pickup: false                  # free store pickup instead of home delivery
max_delivery_days: 7
extras: never                         # extended warranty, insurance, subscriptions: never | ask
sizes: {}                             # e.g. {shoes: 43, t-shirt: L}
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

## Online shopping (any store)
The shop agent reads the frontmatter above: preferred stores per category, how many stores to
compare, whether marketplace sellers are fine, pickup vs delivery and sizes. Stores are the notes
in `shops/` (one per site: domains, search URL, playbook). Orders are written to
`episodes/shopping/` and link the product note and the store note.

## Notes for the agent
<!-- free text: e.g. "buy bananas green", "never the 6-pack of water, too heavy" -->
