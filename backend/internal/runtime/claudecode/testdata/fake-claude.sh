#!/bin/sh
# Fake Claude Code CLI emitting realistic stream-json for tests.
# Modes (FAKE_CLAUDE_MODE): success (default), send, error, crash, hang,
# permission (calls the Nexus MCP permission tool + a Nexus tool with curl),
# idle (waits for one more stdin line, then succeeds), ratelimit / limitok
# (usage limit as error / as a "successful" result), nologin,
# partial (stream_event deltas; FAKE_DELAY seconds between them), ask
# (AskUserQuestion permission prompt), bgbash (Bash run_in_background).
# --resume <id> continues session <id>; --resume gone fails like a
# deleted session.
if [ "$1" = "--version" ]; then
  printf "%s\n" "9.9.9 (Claude Code)"
  exit 0
fi
[ -n "$FAKE_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_ARGS_FILE" && pwd >> "$FAKE_ARGS_FILE"

say() { printf "%s\n" "$1"; }

IFS= read -r first
[ -n "$FAKE_STDIN_FILE" ] && printf '%s\n' "$first" >> "$FAKE_STDIN_FILE"

# --resume <id>: "gone" fails like a deleted session; others continue it.
resume=""; prev=""
for a in "$@"; do [ "$prev" = "--resume" ] && resume="$a"; prev="$a"; done
if [ "$resume" = "gone" ]; then
  say 'No conversation found with session ID: gone' >&2
  exit 1
fi
sid="${resume:-sess-${FAKE_SESSION:-1}}"

say '{"type":"system","subtype":"init","cwd":"/tmp","session_id":"'"$sid"'","tools":["Bash","Read"],"mcp_servers":[],"model":"'"${FAKE_MODEL:-claude-sonnet-4-5}"'","permissionMode":"default","apiKeySource":"none"}'

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
ratelimit)
  say '{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|1767225600","session_id":"sess-1"}'
  ;;
limitok)
  say '{"type":"result","subtype":"success","is_error":false,"result":"5-hour limit reached ∙ resets 3pm","session_id":"sess-1"}'
  ;;
nologin)
  say 'Invalid API key · Please run /login' >&2
  exit 1
  ;;
hang)
  sleep 30
  ;;
partial)
  # --include-partial-messages: deltas, then the full message (must not
  # duplicate), a tool call, then a second streamed block.
  d="${FAKE_DELAY:-0}"
  ev() { say '{"type":"stream_event","event":'"$1"',"parent_tool_use_id":null,"session_id":"'"$sid"'"}'; }
  ev '{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929"}}'
  ev '{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}'
  for w in "Hello" " from" " Nexus"; do
    ev '{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'"$w"'"}}'
    sleep "$d"
  done
  if [ -n "$resume" ]; then ev '{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" (resumed '"$resume"')"}}'; fi
  ev '{"type":"content_block_stop","index":0}'
  full="Hello from Nexus"; [ -n "$resume" ] && full="$full (resumed $resume)"
  say '{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"'"$full"'"}]},"parent_tool_use_id":null,"session_id":"'"$sid"'"}'
  # A subagent's stream events are ignored; its full message is kept.
  say '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"SUB"}},"parent_tool_use_id":"toolu_x","session_id":"'"$sid"'"}'
  ev '{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}'
  ev '{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Second."}}'
  ev '{"type":"content_block_stop","index":1}'
  say '{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"Second. Plus tail"}]},"parent_tool_use_id":null,"session_id":"'"$sid"'"}'
  say '{"type":"result","subtype":"success","is_error":false,"result":"'"$full"'\n\nSecond. Plus tail","session_id":"'"$sid"'","total_cost_usd":0.01,"usage":{"input_tokens":5,"output_tokens":5}}'
  ;;
ask|bgbash)
  cfg=""; prev=""
  for a in "$@"; do [ "$prev" = "--mcp-config" ] && cfg="$a"; prev="$a"; done
  url=$(sed -n 's/.*"url": *"\([^"]*\)".*/\1/p' "$cfg")
  post() { curl -sS --noproxy '*' -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data "$1" "$url"; }
  post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fake-claude","version":"1"}}}' > /dev/null
  post '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null
  if [ "$FAKE_CLAUDE_MODE" = ask ]; then
    input='{"questions":[{"question":"Which category?","header":"Product/category","options":[{"label":"Promotions","description":"Browse current promotions"},{"label":"Laptops","description":"Only laptops"}],"multiSelect":false},{"question":"Which shops?","header":"Shops","options":[{"label":"Worten","description":""},{"label":"Fnac","description":""}],"multiSelect":true}]}'
    tool=AskUserQuestion
  else
    input='{"command":"sleep 100","run_in_background":true}'
    tool=Bash
  fi
  resp=$(post '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nexus_permission","arguments":{"tool_name":"'"$tool"'","input":'"$input"',"tool_use_id":"toolu_q"}}}')
  [ -n "$FAKE_RESP_FILE" ] && printf '%s\n' "$resp" > "$FAKE_RESP_FILE"
  say '{"type":"result","subtype":"success","is_error":false,"result":"asked","session_id":"'"$sid"'"}'
  ;;
idle)
  if IFS= read -r second; then
    say '{"type":"result","subtype":"success","is_error":false,"result":"idle done","session_id":"sess-1"}'
  fi
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
