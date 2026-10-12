#!/bin/sh
# Fake Claude Code CLI emitting realistic stream-json for tests.
# Modes (FAKE_CLAUDE_MODE): success (default), send, error, crash, hang,
# permission (calls the Nexus MCP permission tool + a Nexus tool with curl),
# idle (waits for one more stdin line, then succeeds).
if [ "$1" = "--version" ]; then
  printf "%s\n" "9.9.9 (Claude Code)"
  exit 0
fi
[ -n "$FAKE_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_ARGS_FILE" && pwd >> "$FAKE_ARGS_FILE"

say() { printf "%s\n" "$1"; }

IFS= read -r first
[ -n "$FAKE_STDIN_FILE" ] && printf '%s\n' "$first" > "$FAKE_STDIN_FILE"

say '{"type":"system","subtype":"init","cwd":"/tmp","session_id":"sess-1","tools":["Bash","Read"],"mcp_servers":[],"model":"claude-sonnet-4-5","permissionMode":"default","apiKeySource":"none"}'

case "${FAKE_CLAUDE_MODE:-success}" in
success)
  say '{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Let me look."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls","description":"List files"}}],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":5}},"parent_tool_use_id":null,"session_id":"sess-1"}'
  say 'this is not json'
  say '{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"a.txt\nb.txt","is_error":false}]},"parent_tool_use_id":null,"session_id":"sess-1"}'
  say '{"type":"assistant","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/nope"}}]},"session_id":"sess-1"}'
  say '{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":[{"type":"text","text":"File does not exist."}],"is_error":true}]},"session_id":"sess-1"}'
  say '{"type":"stream_event","event":{"type":"content_block_delta"}}'
  say '{"type":"assistant","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"There are two files."}]},"session_id":"sess-1"}'
  say '{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"duration_api_ms":1000,"num_turns":3,"result":"There are two files.","session_id":"sess-1","total_cost_usd":0.0123,"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":40}}'
  ;;
send)
  say '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Working…"}]},"session_id":"sess-1"}'
  IFS= read -r second
  text=$(printf '%s' "$second" | sed -n 's/.*"text":"\([^"]*\)".*/\1/p')
  say '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"got: '"$text"'"}]},"session_id":"sess-1"}'
  say '{"type":"result","subtype":"success","is_error":false,"result":"got: '"$text"'","session_id":"sess-1","total_cost_usd":0.5,"usage":{"input_tokens":1,"output_tokens":1}}'
  ;;
error)
  say '{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":10,"session_id":"sess-1","total_cost_usd":0.2,"usage":{"input_tokens":5,"output_tokens":5}}'
  ;;
crash)
  say 'boom: something broke' >&2
  exit 3
  ;;
hang)
  sleep 30
  ;;
idle)
  IFS= read -r second
  say '{"type":"result","subtype":"success","is_error":false,"result":"idle done","session_id":"sess-1"}'
  ;;
permission)
  cfg=""; prev=""
  for a in "$@"; do [ "$prev" = "--mcp-config" ] && cfg="$a"; prev="$a"; done
  url=$(sed -n 's/.*"url": *"\([^"]*\)".*/\1/p' "$cfg")
  post() { curl -sS --noproxy '*' -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data "$1" "$url"; }
  post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fake-claude","version":"1"}}}' > /dev/null
  post '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null
  say '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9","name":"Bash","input":{"command":"git push origin main"}}]},"session_id":"sess-1"}'
  resp=$(post '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nexus_permission","arguments":{"tool_name":"Bash","input":{"command":"git push origin main"},"tool_use_id":"toolu_9"}}}')
  case "$resp" in
    *'behavior\":\"allow'*) verdict=allow ;;
    *'behavior\":\"deny'*) verdict=deny ;;
    *) verdict="error: $resp" ;;
  esac
  mem=$(post '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"milk"}}}')
  case "$mem" in *'milk!'*) mem=ok ;; *) mem="error: $mem" ;; esac
  say '{"type":"result","subtype":"success","is_error":false,"result":"verdict: '"$verdict"' memory: '"$mem"'","session_id":"sess-1"}'
  ;;
esac
# Like the real CLI in stream-json input mode: wait for stdin to close.
cat > /dev/null
