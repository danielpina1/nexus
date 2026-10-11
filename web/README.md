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
npm run dev:mock     # same, with VITE_MOCK=1 (no Hub needed)
```

To point the dev proxy at another Hub, set `NEXUS_HUB=http://host:7700 npm run dev`. You can also
set **Settings → Hub URL** in the app; requests then go straight to that origin, and the Hub must
allow CORS from the dashboard origin.

**Auth.** **Settings → Access token** is stored in `localStorage` and sent as
`Authorization: Bearer <token>` on every request. In dev, this is `security.dev_token` from
`nexus.yaml`.

## Mock mode

With `VITE_MOCK=1` (or **Settings → Mock mode**, which reloads the page), the app uses an
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
  and anything else goes to the assistant.

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
  interceptor) and typed clients for every service. Mock mode swaps in the router transport.
- **`src/lib/events.ts`** runs `EventService.Subscribe(since_seq)` with exponential backoff and
  jitter (0.5 s up to 30 s). It reconnects early when the tab comes back online or becomes
  visible. Events are batched into the store every 80 ms. The last seq is persisted together with
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
| `⌘K` / `Ctrl+K` | Command palette: navigate, ask Nexus, run with a specific agent, search notes |
| `/` | Focus the command bar |
| `g` then `o`, `i`, `c`, `t`, `a`, `v`, `m`, `$`, `s` or `,` | Go to a page |
| `?` | List the shortcuts |
