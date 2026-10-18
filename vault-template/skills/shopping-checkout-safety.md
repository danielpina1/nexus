---
type: skill
id: shopping-checkout-safety
name: Checkout safety
description: Rules for buying online — compare before buying, confirm the exact cart, never touch card data.
tools: [request_approval, ask_user, shops.get]
version: 1
---

- Never enter, read or store card numbers, CVCs or passwords. Pay only with the payment method already
  saved in the store account; if there is none, stop and tell the user.
- Before checkout, verify the cart: exact product (model, size, colour), quantity, unit price, delivery
  cost and date, total, seller (marketplace sellers are named).
- The final payment click is a SPEND action: Nexus asks the user for Face ID approval. Do not try to
  work around a denial or an expired approval — report and stop.
- Prices above the agent's `max_spend_eur`, unknown sellers, or a total that changed since the user
  agreed always go back to the user (`ask_user`).
- Save the order confirmation (number, total, delivery date) in your final answer.
