---
type: shop
id: auchan
name: Auchan
domains: [auchan.pt]
country: PT
currency: EUR
categories: [groceries, drugstore, home, electronics, appliances, toys]
search_url: "https://www.auchan.pt/pt/pesquisa?q={q}"   # confidence: medium
login_required: true
checkout_labels: ["Concluir pagamento"]
priority: 2
notes: Hypermarket with groceries and a non-food catalogue. Home delivery and store pickup
  ("Drive"/"Recolha"); delivery options depend on the postal code.
---

# Auchan

Hypermarket (Auchan Portugal): groceries plus electronics, appliances and home goods.

## Login
- Auchan account; log in once in the `auchan` profile.

## Search and products
- Header search box; if the search URL gives a 404, use the box.
- Some non-food items are sold by marketplace partners ("Vendido por …") with separate
  shipping — mention the seller and delivery time in comparisons.

## Cart and checkout
1. "Adicionar" → cart → "Finalizar compra" (SPEND approval).
2. Choose delivery (slot) or pickup; minimum order and fees are shown in the cart.
3. Pay with the saved method only.

## Quirks
- Postal-code prompt before stock/prices for groceries.
- Cookie banner on first load.

## Lessons learned
