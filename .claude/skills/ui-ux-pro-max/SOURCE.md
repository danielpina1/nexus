# Source

Vendored from https://github.com/nextlevelbuilder/ui-ux-pro-max-skill (MIT, see LICENSE),
commit 56ea9cf, path .claude/skills/ui-ux-pro-max (tests omitted).

Local change: SKILL.md script paths point at `.claude/skills/ui-ux-pro-max/scripts/search.py`
(run from the Nexus repo root) instead of ${CLAUDE_PLUGIN_ROOT}, because this is installed as a
project skill rather than a plugin.

Reviewed before vendoring: scripts use only the Python standard library, make no network calls,
start no processes, and write files only with `--persist` (design-system/<project>/MASTER.md).
