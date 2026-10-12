---
type: shop
id: fnac
name: Fnac
domains: [fnac.pt]
country: PT
currency: EUR
categories: [books, music, movies, electronics, computers, gaming, phones, toys]
search_url: "https://www.fnac.pt/SearchResult/ResultList.aspx?Search={q}"   # confidence: medium (same platform as fnac.com)
login_required: true
checkout_labels: ["Concluir pagamento", "Confirmar e pagar"]
priority: 11
notes: Books, music, film and electronics. Member card ("Cartão Fnac") prices/discounts are shown
  separately; marketplace sellers exist. Home delivery or free store pickup.
---

# Fnac

Books, music, film, games, electronics.

## Login
- Fnac account; log in once in the `fnac` profile. Member discounts only show when logged in.

## Search and products
- For books, search by ISBN when known (most precise), else title + author.
- Product pages show "Vendido e enviado por Fnac" vs marketplace sellers; new vs used offers.
- "Preço Aderente" (member price) vs normal price: report both, say which applies.

## Cart and checkout
1. "Adicionar ao cesto/carrinho" → cart → "Finalizar encomenda" (SPEND approval).
2. Delivery to address or "Levantamento em loja" (usually free).
3. Saved payment method only.

## Lessons learned
