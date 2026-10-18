#!/usr/bin/env bash
# Copy the display names (`name:` line) of the starter agents and teams from
# vault-template/ into an existing vault. Only the name line changes; ids,
# instructions, tools and your own edits stay as they are. The Hub re-indexes
# the notes by itself.
#
#   scripts/mac/sync-agent-names.sh [VAULT]   (default ~/Nexus/vault)
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VAULT="${1:-$HOME/Nexus/vault}"
[[ -d "$VAULT" ]] || { echo "no vault at $VAULT" >&2; exit 1; }
n=0
for src in "$REPO_ROOT"/vault-template/agents/*.md "$REPO_ROOT"/vault-template/teams/*.md; do
  rel="${src#"$REPO_ROOT"/vault-template/}"
  dst="$VAULT/$rel"
  [[ -f "$dst" ]] || continue
  name="$(grep -m1 '^name: ' "$src" || true)"
  [[ -n "$name" ]] || continue
  if ! grep -qxF "$name" "$dst"; then
    awk -v line="$name" 'BEGIN{done=0} /^name: / && !done {print line; done=1; next} {print}' "$dst" > "$dst.tmp" && mv "$dst.tmp" "$dst"
    echo "  $rel → ${name#name: }"
    n=$((n + 1))
  fi
done
echo "updated $n note(s) in $VAULT"
