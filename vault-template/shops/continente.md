---
type: shop
id: continente
name: Continente
domains: [continente.pt]
country: PT
currency: EUR
categories: [groceries, drugstore, home, pets]
search_url: "https://www.continente.pt/pesquisa/?q={q}"   # confidence: high (verify if search breaks)
login_required: true
checkout_labels: ["Concluir pagamento", "Confirmar e pagar"]
priority: 1
notes: Supermarket (Continente Online). Delivery slots or Click & Go pickup; logged-in account
  with saved address, Cartão Continente and payment method.
---

# Continente

Supermarket (Sonae). Specialised agent: [[grocery-continente]] (shopping lists, "the usual").

## Login
- Needs a Continente account. Log in once in the `continente` browser profile (see
  docs/SHOPPING.md → Logins). The header shows the account name / "Olá …" when logged in.
- The Cartão Continente balance and coupons are applied in the account; never type card data.

## Search and products
- Search box in the header, or open the search URL directly.
- Product tiles show price per unit and per kg/L ("€/kg"); compare unit prices, not pack prices.
- Promotions: "Promoção", "Leve 2 pague 1", Cartão Continente discount (credited to the card,
  not deducted from the total) — mention both in the summary.
- Out-of-stock tiles show "Indisponível" / "Esgotado".

## Cart and checkout
1. Add with "Adicionar"; quantity steppers on the tile and in the cart.
2. Cart icon → review → "Finalizar compra" (gated SPEND approval) → choose delivery slot or
   Click & Go store → payment with the saved method → confirm.
3. There is a minimum order value for home delivery and a delivery fee that depends on the slot
   (values change — read them on the page, do not assume).
4. Substitutions: the checkout may offer "Aceitar substituições" per item — follow
   [[Shopping preferences]].

## Quirks
- Cookie banner on first load ("Aceitar" / "Rejeitar"): use the banner's own buttons.
- Postal-code pop-up may ask for a delivery area before showing stock.
- Slots fill up early on weekends; if none fits the preferences, ask the user.

## Lessons learned
