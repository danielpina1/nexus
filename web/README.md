# Nexus web dashboard

React dashboard for the Nexus Hub: a live wall of running agents, the inbox (approvals, questions,
notices), chat with a live run view, tasks and their traces, agent editing, the vault (notes and
graph), machines (with a file browser), costs and security.

The dashboard talks **only to the Hub API**: Connect RPC under `/nexus.v1.*`. Node files, node
status and agent runs all come through Hub services. It never connects to nodes or model
providers directly.

## Stack

| | |
|---|---|
| Build | Vite 8, TypeScript 6 (strict) |
| UI | React 19, Tailwind CSS 4 (`@tailwindcss/vite`), lucide-react |
| Routing / data | React Router 7 (lazy, code-split routes), TanStack Query 5, zustand 5 |
| API | `@connectrpc/connect` + `@connectrpc/connect-web` 2.x, `@bufbuild/protobuf` 2.16 |
| Markdown | react-markdown + remark-gfm, with custom `[[wikilink]]` handling |

The generated client lives in `src/gen/nexus/v1` (protoc-gen-es v2). Do not edit it; run `make gen`
from the repo root instead.

## Develop

```sh
npm install
npm run dev          # http://localhost:5173, proxies /nexus.v1.* to http://localhost:7700
npm run dev:mock     # same, with VITE_MOCK=1 (no Hub needed); or "Use demo mode" on the Connect screen
```

To point the dev proxy at another Hub, set `NEXUS_HUB=http://host:7700 npm run dev`. You can also
set **Settings → Hub URL** in the app; requests then go straight to that origin, and the Hub must
allow CORS from the dashboard origin.

**Auth.** The **Connect** screen (first run) or **Settings → Access token** stores the token in `localStorage`; it is sent as
`Authorization: Bearer <token>` on every request. In dev, this is `security.dev_token` from
`nexus.yaml`.

## Mock mode

With `VITE_MOCK=1` (or **Use demo mode** on the Connect screen, or **Settings → Demo mode**, which reloads the page), the app uses an
in-memory Hub built on `createRouterTransport`, so it is fully demoable without a backend:

- **Agents** are the real notes from `../vault-template/agents/*.md`, bundled at build time and
  parsed by the same frontmatter parser the editor uses.
- **Vault** is the template vault plus extra fixture notes (people, products, machines, episodes,
  a daily note). Wikilinks and backlinks are resolved, and the graph is built from them.
- **Nodes:** `mac-studio`, `vps` and `gpu-box`, with runtimes, live CPU/memory telemetry and a
  fake file tree.
- **History:** about 14 days of finished tasks with `CostRecorded` events, which feed the Costs
  page.
- **Live simulation:**
  - A groceries workflow (with a task tree) is filling a Continente cart, streaming a line per
    item and a tool call per search. When the cart is full it asks for a **Face ID** checkout
    approval with a cart screenshot. Mock "Daniel" approves it on the iPhone about 75 s later.
  - The code fixer waits for an `approve`-level push approval (with a diff). Approving it pushes
    and opens a PR.
  - Ops waits for a biometric approval to free disk space.
  - The assistant asks which dentist slot to book (choice buttons).
  - The librarian streams output every second, in a loop.
- **Commands:** `CommandService.Execute` routes by keyword. "fix…" goes to the code fixer,
  "groceries/continente" to grocery (asks a question), "server/disk" to ops, "what/who…" runs a
  vault search, "remember…" does a capture, very short input makes the router ask for an agent,
  and anything else goes to the assistant. `@assistant stress 5000` streams 5000 chunks as fast as possible
  (perf testing).

The mock code is lazy-loaded (`src/mock/*`) and never ends up in the main bundle. In mock mode
the event cache is not persisted, because the mock Hub restarts on every load.

## Build and checks

```sh
npm run build        # tsc -b && vite build → dist/
npm run typecheck
npm run lint         # ESLint flat config (typescript-eslint, react-hooks, react-refresh)
npm run preview      # serve dist/ (with the same /nexus.v1.* proxy)
```

## How nexusd serves it

`nexusd` embeds `web/dist` and serves it from the same origin as the API:

- `/nexus.v1.*` goes to the Connect handlers.
- `/assets/*` holds hashed, immutable files (cache them forever).
- Any other `GET` falls back to `index.html`, because routing is client-side (`/tasks/:id`,
  `/vault?note=…`).

Because it is same-origin, the default Hub URL (empty) works with no configuration. When the
Hub is upgraded and an old tab requests a chunk that no longer exists, the error screen offers a
reload.

## Architecture notes

- **`src/lib/api.ts`** builds one transport (Connect, `baseUrl` from settings, bearer
  interceptor) and typed clients for every service. Vault, agent and device clients load their descriptors
  lazily (unary methods only). Mock mode swaps in the router transport.
- **`src/lib/events.ts`** runs the one shared `EventService.Subscribe(since_seq)`; see "Real time and
  performance" below. The last seq is persisted together with
  a compact snapshot of derived state (tasks, inbox, node status, costs), so a reload resumes the
  stream without losing the inbox. Task pages open an extra stream filtered by
  `task_ids=[id]` from seq 0 to load the full timeline; timelines dedupe by seq.
- **`src/lib/store.ts`** is a zustand store with incremental indexes: tasks, per-task timelines,
  approvals, questions, notices, node status and costs. **`src/lib/selectors.ts`** derives the
  ranked inbox, running tasks, run summaries and task trees.
- **Approvals** at `approve` level are decided from the web with `ApprovalService.Decide`
  (unsigned decision plus challenge). Biometric approvals show **Approve on iPhone**: they must be
  signed by the Secure Enclave key, which the web cannot do. They can still be denied from the
  web.

## Keyboard

| Keys | Action |
|---|---|
| `⌘K` / `Ctrl+K` | Command palette: ask Nexus, `@agent …` to run with an agent, search notes, jump to any page, task or agent, and run actions (theme, reconnect, resync, demo mode, sign out, kill switch) |
| `/` | Focus the command bar (with `@` agent completion) |
| `g` then `h` `i` `c` `w` `k` `m` `s` | Go to Home, Inbox, Chat, Work, Knowledge, Machines or Settings (`g t` Tasks, `g a` Agents, `g v` Vault, `g $` Spend) |
| `` ` `` | Toggle the performance HUD |
| `?` | List the shortcuts |

## Design system

See [`../design/MASTER.md`](../design/MASTER.md). Every colour, radius, duration and size comes from
[`../design/tokens.json`](../design/tokens.json): `npm run tokens` generates `src/styles/tokens.css` and checks
WCAG contrast. It runs automatically before `dev`, `build` and `typecheck`, which fails if the CSS is stale. The
theme follows the system by default; Light, System and Dark are in the sidebar footer. Dark is OLED. Headings use
Montserrat and the body uses Inter (both self-hosted). Dialogs and tabs are shadcn/ui-style components on Radix
primitives in `src/components/ui`.

## Adding a page or section

Pages and navigation come from one registry, [`src/app/registry.ts`](src/app/registry.ts). Routes, the sidebar,
the phone tab bar and More sheet, section tabs, `g`-shortcuts, ⌘K "Go to" commands and hover prefetch are all
derived from it. To add Teams:

```ts
// PAGES
teams: { path: "teams", load: () => import("../pages/Teams") },
// SECTIONS → work.tabs, and add "/teams" to work.match
{ to: "/teams", label: "Teams", page: "teams" },
```

## Real time and performance

- **One stream.** `src/lib/events.ts` holds a single `EventService.Subscribe(since_seq)` for the whole app:
  - It reconnects with exponential backoff and jitter, from 0.5 s up to 30 s.
  - Unauthenticated stops the loop and shows the Connect screen.
  - The Hub sends no headers or heartbeats until the first event, so a cheap unary probe (`ListNodes`) confirms
    "Live" and checks every 45 s that the connection is still alive.
  - It resubscribes from the last seq when the tab becomes visible after more than 15 s, when the browser comes
    back online, when the page is restored from the back/forward cache, or when a timer notices the machine slept.
    It does not resubscribe while the browser reports offline.
  - After catching up it invalidates the active TanStack queries.
- **Batching.** Events are applied once per animation frame (a timer covers hidden tabs). The store keeps records by
  id. Timelines are merged once per task per batch and dedupe by seq. Long streams are **coalesced**: consecutive
  output chunks merge into one event that remembers the seq range it covers, so a 50 000-chunk run stays complete
  and bounded.
- **Streaming rendering.** `StreamingMarkdown` splits output into blocks at blank lines outside code fences. Finished
  blocks are memoized and never re-parsed; only the tail re-renders. Off-screen blocks use `content-visibility:
  auto`. Auto-scroll sticks to the bottom (ResizeObserver) until you scroll up, then shows a "Latest" pill.
- **Optimistic actions.** Approve, deny, answer, reply and cancel update the UI at once, roll back if the Hub
  refuses, and show an error toast with Retry.
- **Lists.** Lists over 80 rows (tasks, the event timeline) are virtualized with `@tanstack/react-virtual`.
- **Caching.** Queries are invalidated by events (tasks, nodes, devices, vault after a task succeeds), not polled.
  Route chunks are code-split and prefetched on hover, focus and idle. The command palette and dialogs are lazy, and
  the large vault/agent/device descriptors load on their first call.
- **Measuring.** Press `` ` `` for the HUD (fps, worst frame, events/s, store ingest ms, DOM nodes, heap).
  `npm run perf -- <url> [chunks] [chat|task]` streams N chunks in demo mode and prints a summary
  (`window.__nexusPerf` and `window.__nexusHud` expose the same data).

## Testing against a real Hub (macOS or Linux)

```sh
make web                                   # or: cd web && npm ci && npm run build
scripts/mac/run-local.sh                   # Hub + node on https://localhost:7701; prints the dev token
open "https://localhost:7701/#token=<dev token>"   # the #token= fragment signs you in, then disappears
```

The Hub uses its own private CA, so the browser warns once. In Chrome, choose Advanced → Proceed (or trust
`<dir>/hub/data/security/pki/ca.crt` in Keychain Access). Without `#token=`, the **Connect** screen asks for the token
and also offers **Use demo mode**.

**End-to-end smoke test.** It drives Chromium against the real Hub through the Connect screen, a chat run with
`@echo … ?approve`, an approval from the Inbox, the task page and timeline, a vault search, the machine file browser,
the ⌘K palette, a reload that resumes from the cached seq, and offline→online catch-up:

```sh
cd web
npm i --no-save playwright@1.56.1 && npx playwright install chromium   # once
npm run e2e -- https://127.0.0.1:7701 <dev token>
# from the repo root: node web/e2e/smoke.mjs https://127.0.0.1:7701 <dev token>
# E2E_CHANNEL=chrome uses the installed Google Chrome; HEADED=1 shows the window
```
