---
type: shop
id: kuantokusta
name: KuantoKusta
domains: [kuantokusta.pt]
country: PT
currency: EUR
categories: [price-comparison, electronics, computers, appliances, home, books, toys, sports]
search_url: "https://www.kuantokusta.pt/search?q={q}"   # confidence: medium
login_required: false
research_only: true
checkout_labels: []
priority: 5
notes: Portuguese price-comparison site. Research only — find which stores sell an item and at
  what price, then buy at the store itself (shops.get that store). Never buy through it.
---

# KuantoKusta

Price comparison across Portuguese stores. **Research only: no cart, no checkout.**

## How to use
1. Search for the exact product (brand + model; EAN if known).
2. Open the product page: it lists offers per store with price, shipping cost and stock.
   Note the cheapest 3–5 offers (store, price, shipping, availability, "preço total").
3. Prices here can lag behind the stores. Before recommending, confirm the price on the store's
   own site (shops.get the store, open the product there).
4. Offers from stores without a note in `shops/` can be reported, but buying there needs a new
   shop note (or `browser.allow_domain` approved by the user).

## Quirks
- "Ir para a loja" links redirect through kuantokusta.pt to the store; the store domain must be
  allowed for the run (shops.get it first) or the navigation is blocked.

## Lessons learned
