# Open Questions

Decisions that need input before or during Phase 1. Defaults in **bold** are what we will assume
until answered.

| # | Question | Default assumption |
|---|---|---|
| 1 | **Unify Gateway** — own service or unify.ai? API shape (OpenAI-compatible? Anthropic-compatible?), auth, base URL, model alias names, cost reporting? | **OpenAI-compatible `/v1/chat/completions` with bearer token; model names are gateway aliases.** |
| 2 | **Anthrex** — what does it do and how is it invoked (HTTP API, CLI, MCP, queue)? Does it stream? | **Treated as an external runtime behind an adapter; no changes to the Anthrex repo.** |
| 3 | **Existing "Nexus agents"** — do any already exist as code/prompts to import? | **Start fresh; import later as agent notes.** |
| 4 | **Where does the Hub run?** VPS vs home machine. | **Small VPS with public HTTPS; Nodes dial out; Tailscale optional.** |
| 5 | **Which machines become Nodes first?** (Mac? Linux server? GPU box for local models?) | **One Mac (Claude Code, Codex, browser) + one Linux box/VPS.** |
| 6 | **Local models** — which machine runs Ollama, what hardware? | **Ollama on the strongest local machine; small model for routing/tagging, an embedding model.** |
| 7 | **Single user or multi-user?** | **Single user, multiple devices; data model keeps an `owner` field for later.** |
| 8 | **Vault** — start fresh or import an existing Obsidian vault? Git remote (GitHub private repo?) | **Fresh vault from `vault-template/`; private git remote.** |
| 9 | **Apple Developer account** for APNs, App Groups, TestFlight. | **Needed before Phase 1 iOS push work.** |
| 10 | **Language of the UI and agents** — Portuguese, English, both? | **English UI, agents answer in the language you write in.** |
| 11 | **Continente automation** — account with saved payment method and delivery address; acceptable to automate under their terms? | **Human biometric approval before checkout; no card data handled by agents.** |
| 12 | **Codegen output** — commit generated code (`gen/`) or generate in CI? | **Commit generated code for Go/TS/Swift so clients build without tooling.** |
