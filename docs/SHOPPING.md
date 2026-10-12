# Shopping

> Status: v0.1 — shop agent, store notes, price compare. Stores are Portuguese/European sites.

"Buy me noise-cancelling headphones under 300 €" → Nexus picks suitable stores, compares price,
delivery and stock, asks where to buy, fills the cart, and checks out **only after a biometric
approval** showing the store, items, total and a screenshot of the cart.

## 1. Pieces

| Piece | Where | What it does |
|---|---|---|
| Store notes | vault `shops/<id>.md` | One note per site: domains (allow-list), categories, search URL, checkout labels, playbook. |
| `shops` hub module | `backend/internal/shops` | Hub tools `shops.list {category?, ids?, buyable?, limit?, query?}` and `shops.get {id, query?}` (both READ) reading `shops/*.md`. |
| `shop` agent | vault `agents/shop.md` | General shopper (runtime `browser`): research → compare → ask → cart → approval → order → episode note. |
| `grocery-continente` agent | vault `agents/grocery-continente.md` | Specialised supermarket agent (lists, "the usual", substitutions) for one store. Kept on purpose: grocery runs need list/preference logic that the general agent does not. It shares the `continente` browser profile with the shop agent. |
| Workflows | vault `workflows/shop.md`, `price-compare.md`, `groceries.md` | Intent entry points ("buy", "comprar", "compare prices", "melhor preço", "buy the usual"). |
| Browser runtime | `backend/internal/runtime/browser` | Drives Chrome; enforces the domain allow-list, per-store profiles and the checkout (SPEND) gate. |
| Preferences | vault `templates/Shopping preferences.md` | Budget, preferred stores per category, marketplace sellers, pickup, sizes, substitutions. |

## 2. How a purchase runs

```mermaid
sequenceDiagram
  participant U as User (phone)
  participant H as Hub (tasks, shops, approvals)
  participant N as Node (browser runtime + Chrome)
  U->>H: "comprar Sony WH-1000XM5 até 350 €"
  H->>N: RunAssignment (agent shop; context.browser / shops / shopping from the agent note)
  N->>H: shops.list {category: electronics}
  N->>H: shops.get {id: worten, query}  (wrapped: worten.pt joins the allow-list)
  N->>N: navigate worten.pt with profile "worten" (own Chrome, own login)
  N->>H: shops.get fnac, … (one profile/Chrome per store)
  N->>H: ask_user "Buy at Worten for 329,99 €?" → U picks
  N->>N: add to cart, screenshot
  N->>H: Approval SPEND (store, items, total, page text, screenshot) → U Face ID
  N->>N: click final button, extract confirmation
  N->>H: vault.write episodes/shopping/… , notify
```

1. **Agent config reaches the node.** The Agent proto has no `browser:` field, so the tasks
   engine copies the agent note's `browser`, `shops` and `shopping` frontmatter keys into
   `RunAssignment.context` under the same names (`internal/tasks/agentconfig.go`). Agent values
   replace task params of the same name — a task cannot loosen an agent's allow-list.
2. **Stores are learned at run time.** The hub can read shop notes, the node cannot; so the
   agent calls `shops.get` before opening a store. On the node the browser runtime wraps that
   hub tool: every store it returns is registered with the run — its `domains` join the run's
   allow-list, its `checkout_labels` join the SPEND patterns, and navigations to its domains use
   the store's own browser profile.
3. **Everything else needs approval.** A shopping run (one that has `shops.get`, or
   `browser.restrict_domains: true`) is restricted from the start, even with an empty list.
   Opening a domain that is neither in `browser.allowed_domains` nor a fetched store fails with
   a hint; the agent may call `browser.allow_domain {domain, reason}`, a WRITE_EXTERNAL tool —
   the agent loop asks the user (APPROVE by default) before it runs. Bare TLDs and public
   suffixes (`pt`, `com.pt`, `co.uk`…) are refused.
4. **Checkout.** Clicks (or Enter-submits) on elements whose label matches a checkout pattern
   (defaults in `classify.go` + node `extra_checkout_patterns` + the store's `checkout_labels`)
   are SPEND actions: the runtime takes a full-page screenshot and asks for approval
   (BIOMETRIC by default) with a runtime-built payload: `store {id, name, host}`, `total` and
   `items` (best-effort extraction from the page text), `page_text`, `url`, `element`, and the
   `screenshot` attachment id. Nothing in that payload is written by the model.

### Research mode and price comparison

The `price-compare` workflow runs `shops.list {category | ids, buyable: true, limit: 4}`, then in
parallel one `shop` run per store **in research mode** (no cart: price, delivery, total, stock,
seller, URL) plus one run on KuantoKusta (a `research_only` comparison site), has the assistant
write a comparison table, notifies it, asks "buy at X?" and, if the answer is a store (or
"cheapest"), starts a normal `shop` buy run. Each research run is its own task with its own
store profile, so stores are visited concurrently without sharing a Chrome.

The `shop` workflow is the direct path: inputs `item`, `budget`, `store` (empty = compare 2–4
stores and ask), `cheapest` (buy the best total without asking which store).

## 3. Store notes

```markdown
---
type: shop
id: worten                       # = browser profile name; lower-case
name: Worten                     # [[Worten]] links from episodes resolve here
domains: [worten.pt]             # allow-list (subdomains included)
country: PT
currency: EUR
categories: [electronics, appliances, computers, gaming, phones, home]
search_url: "https://www.worten.pt/search?query={q}"   # {q} = URL-encoded query; "" = use the search box
login_required: true
checkout_labels: ["Confirmar e pagar", "/pagar (ja|agora)/"]   # literal labels; /…/ = regexp
research_only: false             # true: price research only (no cart, no checkout)
priority: 10                     # lower first in shops.list
enabled: true
notes: one-line hints shown in shops.list
---
# Worten
Playbook: login, search, product page checks, cart, checkout, delivery, quirks.
## Lessons learned
```

Labels are matched case- and accent-insensitively against the element's visible label (and the
form's submit label for Enter). Literal labels match as whole words.

### Included stores (vault-template/shops)

| id | Store | Categories | Search URL confidence |
|---|---|---|---|
| `continente` | Continente | groceries, drugstore, home | high (`/pesquisa/?q=`) |
| `pingo-doce` | Pingo Doce | groceries | **uncertain channel**: own shop vs Mercadão (`mercadao.pt`); no search URL |
| `auchan` | Auchan | groceries, home, electronics | medium (`/pt/pesquisa?q=`) |
| `worten` | Worten | electronics, appliances | medium-high (`/search?query=`) |
| `fnac` | Fnac | books, music, electronics | medium (`/SearchResult/ResultList.aspx?Search=`) |
| `radio-popular` | Radio Popular | electronics, appliances | none (search box) |
| `pcdiga` | PCDiga (`pcdiga.com`) | computers, components | none (search box) |
| `amazon-es` | Amazon.es | general | high (`/s?k=`) |
| `el-corte-ingles` | El Corte Inglés | general, fashion, beauty | none (search box) |
| `ikea` | IKEA Portugal (`ikea.com`, stay under `/pt/pt/`) | furniture, home | high (`/pt/pt/search/?q=`) |
| `decathlon` | Decathlon | sports | medium (`/search?Ntt=`) |
| `kuantokusta` | KuantoKusta | price-comparison (research only) | medium (`/search?q=`) |

URLs and labels were written from knowledge, not verified live: the agent falls back to the
site's search box and records what it learns under **Lessons learned**. When a store changes,
edit its note — no code changes.

### Adding a store

1. Create `shops/<id>.md` with the frontmatter above (at least `type: shop`, `domains`,
   `categories`) and a short playbook.
2. Check it: `shops.get {id}` from any agent or the dashboard tool runner; invalid notes are
   reported (missing domains, a `search_url` outside the store's domains…).
3. Log in once if the store needs an account (§4). That's it — `shops.list` picks it up by
   category and the shop agent can use it.

Treat `shops/` as configuration: a store's domains are trusted for every shopping run. The shop
agent is told never to write there; on a node where that is not enough, set
`runtime.browser.trust_shop_notes: false` and every store domain then needs a
`browser.allow_domain` approval.

## 4. Logins

Each store keeps its session in a persistent Chrome profile on the node, named after the store
id (`<node data_dir>/runtime.browser/profiles/<id>`), so a login done once survives across runs
and stores never share cookies. A run's other sites use the run's default profile:
`runtime.browser.agents.<agent>.profile` (node config) → the agent note's `browser.profile` →
the agent id. `grocery-continente` sets `browser.profile: continente`, so it shares the
Continente login with the shop agent.

To log in (on the node, from `backend/`, with no run using that profile):

```sh
go run ./internal/runtime/browser/cmd/nexus-browser-login \
    -data-dir <node data_dir> -profile worten -url https://www.worten.pt/
```

Log in, tick "remember me", close the window. When a run finds a store logged out at checkout,
it stops and asks you (`ask_user`) to do this. Agents never type passwords, one-time codes or
card numbers (the runtime refuses password/card fields and Luhn-valid numbers).

Node config (`runtime.browser`):

```yaml
runtime.browser:
  profile_per_shop: true        # one profile per store id (false: the run's default profile)
  shop_profiles: { amazon-es: amazon }   # rename a store's profile
  trust_shop_notes: true        # shops.get domains join the allow-list
  extra_checkout_patterns: []   # more SPEND label regexps for every run
```

## 5. Safety summary

- **Money moves only with your approval:** every checkout/payment click is SPEND → biometric,
  with the store, total, items, page text and screenshot; denial stops the run (the agent is told
  not to retry another way). Agent `limits.max_spend_eur` and the preferences' budget make the
  agent ask before going over.
- **Sites are allow-listed per run:** agent `browser.allowed_domains` + stores fetched with
  `shops.get` + domains you approved with `browser.allow_domain`. Navigation outside is blocked
  both in Chrome (request interception) and after clicks.
- **Page content is untrusted:** the agents' instructions and the runtime guidance say so;
  prompt-injection text on product pages cannot unlock domains or approvals.
- **No secrets in the loop:** logins live in browser profiles you create interactively; payment
  uses the method saved in each account.
- **Upsells:** warranties, insurance, subscriptions and Prime-like trials are declined unless you
  asked (agent instructions + `extras` in the preferences).
- **Records:** each order becomes an episode note in `episodes/shopping/` linking the product
  note (`products/`) and the store note, so "buy the same again" and price history work.

## 6. Limits and next steps

- Item/total extraction for the approval card is a text heuristic; the screenshot and page text
  are the source of truth.
- A run visiting several stores starts one Chrome per store profile (kept until the run ends).
- Ideas: a `price` watcher trigger using research mode; per-store minimum-order/delivery-fee
  fields; verifying search URLs with a scheduled "store health" research run.
