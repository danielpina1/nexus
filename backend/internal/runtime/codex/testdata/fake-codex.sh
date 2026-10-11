#!/bin/sh
# Fake Codex CLI emitting `codex exec --json` JSONL for tests.
# Modes (FAKE_CODEX_MODE): success (default), legacy, fail, crash, hang.
say() { printf "%s\n" "$1"; }
if [ "$1" = "--version" ]; then
  say "codex-cli 0.99.0"
  exit 0
fi
[ -n "$FAKE_ARGS_FILE" ] && printf '%s\n' "$@" > "$FAKE_ARGS_FILE" && pwd >> "$FAKE_ARGS_FILE"
if [ -n "$FAKE_STDIN_FILE" ]; then cat > "$FAKE_STDIN_FILE"; else cat > /dev/null; fi

case "${FAKE_CODEX_MODE:-success}" in
success)
  say '{"type":"thread.started","thread_id":"0199a213-81c0-7800-8aa1-bbab2a035a53"}'
  say '{"type":"turn.started"}'
  say '{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"**Listing files**"}}'
  say '{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","aggregated_output":"","exit_code":null,"status":"in_progress"}}'
  say '{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","aggregated_output":"a.txt\nb.txt\n","exit_code":0,"status":"completed"}}'
  say 'garbage line'
  say '{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"bash -lc false","aggregated_output":"","status":"in_progress"}}'
  say '{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"bash -lc false","aggregated_output":"","exit_code":1,"status":"failed"}}'
  say '{"type":"item.completed","item":{"id":"item_3","type":"file_change","changes":[{"path":"c.txt","kind":"add"}],"status":"completed"}}'
  say '{"type":"item.completed","item":{"id":"item_4","type":"mcp_tool_call","server":"nexus","tool":"memory.search","arguments":{"q":"x"},"result":{"hits":1},"status":"completed"}}'
  say '{"type":"item.completed","item":{"id":"item_5","type":"agent_message","text":"Created c.txt."}}'
  say '{"type":"some.future.event","foo":1}'
  say '{"type":"turn.completed","usage":{"input_tokens":2000,"cached_input_tokens":500,"output_tokens":300}}'
  ;;
legacy)
  say '{"id":"0","msg":{"type":"task_started"}}'
  say '{"id":"0","msg":{"type":"exec_command_begin","call_id":"call_1","command":["bash","-lc","ls"],"cwd":"/tmp"}}'
  say '{"id":"0","msg":{"type":"exec_command_end","call_id":"call_1","stdout":"a.txt\n","stderr":"","exit_code":0}}'
  say '{"id":"0","msg":{"type":"agent_message","message":"Found a.txt"}}'
  say '{"id":"0","msg":{"type":"token_count","input_tokens":10,"output_tokens":4}}'
  say '{"id":"0","msg":{"type":"task_complete","last_agent_message":"Found a.txt"}}'
  ;;
fail)
  say '{"type":"thread.started","thread_id":"t"}'
  say '{"type":"turn.started"}'
  say '{"type":"turn.failed","error":{"message":"stream disconnected before completion"}}'
  exit 1
  ;;
crash)
  say 'fatal: not logged in' >&2
  exit 2
  ;;
hang)
  say '{"type":"thread.started","thread_id":"t"}'
  sleep 30
  ;;
esac
