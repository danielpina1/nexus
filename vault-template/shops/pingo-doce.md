---
type: shop
id: pingo-doce
name: Pingo Doce
domains: [pingodoce.pt, mercadao.pt]
country: PT
currency: EUR
categories: [groceries, drugstore, home]
search_url: ""             # unknown: use the site's search box
login_required: true
checkout_labels: []
priority: 3
notes: UNCERTAIN online channel — historically Pingo Doce groceries were sold online through
  Mercadão (mercadao.pt); check pingodoce.pt for a own online shop first. Adapt live and record
  what works under Lessons learned.
---

# Pingo Doce

Supermarket (Jerónimo Martins). **The online channel is uncertain** — verify on the first run.

## Where to buy
- Start at https://www.pingodoce.pt/ and look for an online shop / "Compras online" link.
- If it points to Mercadão (https://mercadao.pt/), choose the Pingo Doce store there. Mercadão
  is a delivery marketplace: prices may differ slightly from the physical store.
- If neither offers delivery to the user's postal code, report that and suggest another store
  from `shops.list {category: groceries}`.

## Login
- Separate accounts for pingodoce.pt and mercadao.pt may be needed. Log in once in the
  `pingo-doce` profile (docs/SHOPPING.md → Logins).

## Checkout
- Delivery windows and minimum order apply; read them on the page.
- The final confirmation button is gated as SPEND (biometric approval).

## Lessons learned
