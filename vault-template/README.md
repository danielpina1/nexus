# Nexus starter vault

Copy this folder to start a new vault (`nexusd init --vault ./vault` will do it for you).
It is a normal Obsidian vault: open it in Obsidian to browse notes and the graph.

- `agents/` — agent definitions (see `docs/AGENTS.md`)
- `workflows/` — workflow definitions (see `docs/WORKFLOWS.md`)
- `policies/` — trust levels, budgets, model aliases
- `templates/` — note templates used by agents and the apps (`daily.md`, `weekly.md` are used for
  periodic notes; variables: `{{title}}`, `{{date}}`, `{{date:dddd D MMMM YYYY}}`, `{{time}}`,
  `{{yesterday}}`, `{{tomorrow}}`, `{{prompt:Question}}` — see `docs/VAULT.md` §9.2)

Folders Nexus manages for you (hidden, so Obsidian ignores them):

- `.trash/` — deleted notes, restorable for 30 days (also Obsidian's trash folder)
- `.nexus/` — the writer lock and the write-ahead journal that makes multi-file changes
  (renames with link updates, merges) crash-safe. Never commit or sync it.

Optional: list folders under `vault.encryption.folders` (e.g. `private/`) in `nexus.yaml` to keep
their notes encrypted at rest; back up the key (`data/vault/vault.key` or the macOS Keychain)
separately — without it those notes cannot be read.
