---
type: shop
id: amazon-es
name: Amazon.es
domains: [amazon.es]
country: ES
currency: EUR
categories: [general, electronics, computers, books, home, toys, sports, beauty, gaming]
search_url: "https://www.amazon.es/s?k={q}"   # confidence: high
login_required: true
checkout_labels: ["Realizar pedido", "Hacer pedido", "Comprar ya", "Comprar ahora", "Finalizar pedido", "/realizar (el|tu|su) pedido/"]
priority: 20
notes: Ships to Portugal (Spanish store, pages in Spanish; Portuguese/English can be chosen).
  Check "Envío a Portugal", delivery date and seller (Amazon vs third party). Prime may apply.
---

# Amazon.es

General store; the usual Amazon for Portugal.

## Login
- Amazon account; log in once in the `amazon-es` profile ("Mantener la sesión iniciada").
  Amazon may ask for a one-time code: that is the user's job — `ask_user` and wait, never type
  codes yourself.

## Search and products
- Search URL or the search bar. Sponsored results ("Patrocinado") come first — skip them when
  comparing.
- On the product page check: price, "Vendido por" / "Enviado por", delivery date to Portugal
  ("Entrega … en Portugal"), shipping cost, and variant (colour/size/capacity).
- Make sure the delivery address shown in the header is the user's Portuguese address.

## Cart and checkout
- Never use "Comprar ya" (one-click buy): it is gated anyway, but always go through the cart so
  the user sees the full order. Use "Añadir a la cesta".
- Cart ("Cesta") → "Tramitar pedido" → review address, shipping speed, payment →
  "Realizar pedido" / "Comprar" (SPEND approval).
- Decline Prime trials, insurance and add-ons unless asked.

## Lessons learned
