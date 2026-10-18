# Anthrex wire fixtures

Copied verbatim (read-only) from the Anthrex repository, MIT-licensed, same author:

- source: `/home/user/anthrex` (github.com/danielpina1/anthrex)
- commit: `a54e63391de2bd7022665a854758342c047bc516`
- files:
  - `crates/proto/src/m8c_run_info.json`, `m9_run_info.json`, `m9_1_run_info.json`,
    `m912_run_info.json`, `m93_run_info.json`, `m95_run_info.json` — serde_json
    encodings of `proto::RunInfo` at successive protocol milestones.
  - `crates/mcp/src/fixtures/orch_tools.json` — the orchestrator MCP tool table.

serde_json and rmp_serde (`to_vec_named`) share serde's data model, so a JSON
fixture's structure (externally tagged enums: unit variants as strings, struct
variants as single-key maps; internally tagged enums as maps with a tag key) is
exactly what a MessagePack frame carries. The tests convert each fixture to
MessagePack with our encoder, decode it back, compare, and decode it into the
typed Go structs.

Do not edit these files; re-copy them when the protocol version changes.
