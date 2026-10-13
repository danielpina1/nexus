#!/usr/bin/env bash
# Test Nexus end to end on this Mac and write a report you can paste back.
#
#   scripts/mac/selftest.sh [--skip-subscriptions] [--skip-ios] [--skip-web] [--keep]
#
# Installs nothing and does not touch ~/Nexus or the launchd services: it uses
# a temporary Hub + node on 127.0.0.1:7702. Steps (each PASS / FAIL / SKIP):
#   1. tools present        2. backend tests        3. web dashboard build
#   4. temporary Hub + node with TLS + pairing       5. echo task end to end
#   6. shell task waits for approval, then runs      7. runtimes seen by the node
#   8. Ollama via `nexusd llm check`                 9. Claude Code via Nexus (subscription)
#  10. Codex via Nexus (subscription)               11. iPhone app compiles for the Simulator
#  12. NexusKit wire-format tests on a Simulator
# Steps 9-10 send one tiny prompt each ("reply NEXUS-OK"): a few seconds of usage.
# The report is written to ~/Nexus-selftest-<time>.txt and printed at the end.
set -uo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SKIP_SUBS=0 SKIP_IOS=0 SKIP_WEB=0 KEEP=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-subscriptions) SKIP_SUBS=1; shift ;;
    --skip-ios) SKIP_IOS=1; shift ;;
    --skip-web) SKIP_WEB=1; shift ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $1 (see --help)" ;;
  esac
done

load_brew_env 2>/dev/null || true
PORT=7702
STAMP="$(date +%Y%m%d-%H%M%S)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nexus-selftest.XXXXXX")"
REPORT="$HOME/Nexus-selftest-$STAMP.txt"
LOGDIR="$WORK/logs"; mkdir -p "$LOGDIR"
BIN="$WORK/bin"
RESULTS=""          # lines "STATUS|step|detail"
FAILS=0
RUN_PID=""

record() { # status step detail
  RESULTS="${RESULTS}$1|$2|$3
"
  case "$1" in
    PASS) ok "$2 — $3" ;;
    SKIP) warn "$2 — skipped: $3" ;;
    *) err "$2 — $3"; FAILS=$((FAILS + 1)) ;;
  esac
}

# timeout_run SECS LOG cmd...: run cmd with output to LOG, killed after SECS
# (macOS has no coreutils `timeout`). Returns the command's exit status.
timeout_run() {
  local secs="$1" log="$2"; shift 2
  "$@" >"$log" 2>&1 &
  local pid=$! waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [[ "$waited" -ge "$secs" ]]; then
      kill "$pid" 2>/dev/null || true
      echo "(timed out after ${secs}s)" >>"$log"
      return 124
    fi
    sleep 1; waited=$((waited + 1))
  done
  wait "$pid"
}

# excerpt LOG N: last N meaningful lines of a log for the report
excerpt() { grep -v '^\s*$' "$1" 2>/dev/null | tail -n "${2:-25}" | sed 's/^/    /'; }

cleanup() {
  if [[ -n "$RUN_PID" ]]; then
    kill -TERM -- "-$RUN_PID" 2>/dev/null || kill -TERM "$RUN_PID" 2>/dev/null || true
    sleep 1
    kill -KILL -- "-$RUN_PID" 2>/dev/null || true
  fi
  if [[ "$KEEP" == 0 ]]; then rm -rf "$WORK/local"; fi
}
trap cleanup EXIT

say "Nexus self-test — work dir $WORK"
say "Report: $REPORT"

# ---------------------------------------------------------------- 1. tools
step "Tools"
{
  echo "macOS: $(sw_vers -productVersion 2>/dev/null || uname -sr)  arch: $(uname -m)"
  echo "memory: $(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1073741824 )) GB"
  for t in go node npm git ollama claude codex xcodebuild xcodegen buf; do
    if have "$t"; then
      v="$("$t" --version 2>/dev/null | head -n1)"
      [[ "$t" == go ]] && v="$(go version 2>/dev/null)"
      [[ "$t" == xcodebuild ]] && v="$(xcodebuild -version 2>/dev/null | tr '\n' ' ')"
      echo "$t: ${v:-present}"
    else
      echo "$t: MISSING"
    fi
  done
  chrome="$(ls -d "/Applications/Google Chrome.app" 2>/dev/null || true)"
  echo "chrome: ${chrome:-MISSING (/Applications/Google Chrome.app)}"
} | tee "$LOGDIR/tools.txt"
if have go; then record PASS "tools" "go present"; else record FAIL "tools" "go missing (brew install go)"; fi

# ---------------------------------------------------------------- 2. backend tests
step "Backend tests (go test ./...)"
if have go; then
  if (cd "$REPO_ROOT/backend" && go test -count=1 ./... >"$LOGDIR/go-test.log" 2>&1); then
    record PASS "backend tests" "$(grep -c '^ok' "$LOGDIR/go-test.log") packages ok"
  else
    record FAIL "backend tests" "$(grep -c '^FAIL' "$LOGDIR/go-test.log") packages failed (see excerpt)"
  fi
  if grep -q 'no browser found\|chrome not found' "$LOGDIR/go-test.log"; then
    record SKIP "browser tests" "Chrome not detected by the browser runtime"
  fi
else
  record SKIP "backend tests" "go missing"
fi

# ---------------------------------------------------------------- 3. web
step "Web dashboard build"
if [[ "$SKIP_WEB" == 1 ]]; then record SKIP "web build" "--skip-web"
elif have npm; then
  if (cd "$REPO_ROOT/web" && npm ci --no-audit --no-fund >"$LOGDIR/web.log" 2>&1 && npm run build >>"$LOGDIR/web.log" 2>&1); then
    record PASS "web build" "web/dist built"
  else
    record FAIL "web build" "npm ci / build failed"
  fi
else
  record SKIP "web build" "npm missing"
fi

# ---------------------------------------------------------------- 4. local hub + node
step "Temporary Hub + node (TLS, pairing) on 127.0.0.1:$PORT"
HUB_UP=0
if have go && (cd "$REPO_ROOT/backend" && mkdir -p "$BIN" && go build -o "$BIN/" ./cmd/nexusd ./cmd/nexus-node ./cmd/nexus >"$LOGDIR/build.log" 2>&1); then
  set -m
  "$REPO_ROOT/scripts/mac/run-local.sh" --dir "$WORK/local" --port "$PORT" --bin "$BIN" >"$LOGDIR/run-local.log" 2>&1 &
  RUN_PID=$!
  set +m
  NEXUS_HUB_DIR="$WORK/local/hub"
  for _ in $(seq 1 60); do
    if [[ -f "$NEXUS_HUB_DIR/nexus.yaml" ]] && grep -q "node connected\|connected to hub" "$LOGDIR/run-local.log" 2>/dev/null; then HUB_UP=1; break; fi
    if ! kill -0 "$RUN_PID" 2>/dev/null; then break; fi
    sleep 1
  done
  if [[ "$HUB_UP" == 1 ]]; then
    export NEXUS_URL="https://127.0.0.1:$PORT"
    export NEXUS_CA_FILE="$NEXUS_HUB_DIR/data/security/pki/ca.crt"
    NEXUS_TOKEN="$(awk '/^  dev_token:/{print $2; exit}' "$NEXUS_HUB_DIR/nexus.yaml")"
    export NEXUS_TOKEN
    sleep 2
    if "$BIN/nexus" status >"$LOGDIR/status.log" 2>&1 && grep -q "1 online" "$LOGDIR/status.log"; then
      record PASS "hub + node" "TLS hub up, node paired and online"
    else
      record FAIL "hub + node" "node not online (see status)"; HUB_UP=0
    fi
  else
    record FAIL "hub + node" "did not come up within 60 s (see run-local log)"
  fi
else
  record FAIL "hub + node" "build failed"
fi

nexus_cli() { "$BIN/nexus" "$@"; }

# ---------------------------------------------------------------- 5. echo
step "Echo task end to end"
if [[ "$HUB_UP" == 1 ]]; then
  if timeout_run 40 "$LOGDIR/echo.log" nexus_cli run "selftest hello from the Mac" -agent echo -follow && grep -q "succeeded" "$LOGDIR/echo.log"; then
    record PASS "echo task" "streamed and succeeded"
  else
    record FAIL "echo task" "did not succeed"
  fi
else record SKIP "echo task" "no hub"; fi

# ---------------------------------------------------------------- 6. shell approval
step "Shell task waits for approval"
if [[ "$HUB_UP" == 1 ]]; then
  nexus_cli run "echo approved-on-mac" -agent shell -follow >"$LOGDIR/shell.log" 2>&1 &
  spid=$!
  apr=""
  for _ in $(seq 1 20); do apr="$(grep -o 'apr_[A-Z0-9]*' "$LOGDIR/shell.log" | head -n1)"; [[ -n "$apr" ]] && break; sleep 1; done
  if [[ -n "$apr" ]]; then
    nexus_cli approve "$apr" >>"$LOGDIR/shell.log" 2>&1 || true
    for _ in $(seq 1 20); do kill -0 "$spid" 2>/dev/null || break; sleep 1; done
    kill "$spid" 2>/dev/null || true
    if grep -q "approved-on-mac" "$LOGDIR/shell.log" && grep -q "succeeded" "$LOGDIR/shell.log"; then
      record PASS "shell approval" "paused for approval, ran after approve"
    else
      record FAIL "shell approval" "approved but did not complete"
    fi
  else
    kill "$spid" 2>/dev/null || true
    record FAIL "shell approval" "no approval request appeared"
  fi
else record SKIP "shell approval" "no hub"; fi

# ---------------------------------------------------------------- 7. runtimes
step "Runtimes seen by the node"
if [[ -x "$BIN/nexus-node" && -f "$WORK/local/node/node.yaml" ]]; then
  "$BIN/nexus-node" runtimes -config "$WORK/local/node/node.yaml" >"$LOGDIR/runtimes.log" 2>&1 || true
  cat "$LOGDIR/runtimes.log"
  record PASS "runtimes" "$(awk 'NR>1 && $2=="true"{printf "%s ", $1}' "$LOGDIR/runtimes.log")available"
else record SKIP "runtimes" "no node"; fi

# ---------------------------------------------------------------- 8. ollama
step "Ollama (nexusd llm check)"
if [[ -f "$WORK/local/hub/nexus.yaml" ]] && have ollama; then
  if (cd "$WORK/local/hub" && "$BIN/nexusd" llm check -config nexus.yaml -quick >"$LOGDIR/llm.log" 2>&1); then
    record PASS "ollama" "llm check passed"
  else
    record FAIL "ollama" "llm check failed (is Ollama running? models pulled?)"
  fi
  cat "$LOGDIR/llm.log"
else record SKIP "ollama" "ollama missing or no hub"; fi

# ---------------------------------------------------------------- 9-10. subscriptions
sub_test() { # runtime agent-id label
  local rt="$1" id="$2" label="$3" vault="$WORK/local/hub/vault"
  cat >"$vault/agents/$id.md" <<EOF
---
type: agent
id: $id
name: Selftest ($label)
description: Self-test agent that answers with a fixed word.
runtime: $rt
tier: simple
node: { tags: [any] }
tools: []
claude: { model: haiku }
version: 1
---
Answer with exactly NEXUS-OK and nothing else.
EOF
  sleep 3 # vault watcher reload
  timeout_run 240 "$LOGDIR/$id.log" nexus_cli run "Reply with exactly: NEXUS-OK" -agent "$id" -follow
  if grep -q "NEXUS-OK" "$LOGDIR/$id.log" && grep -q "succeeded" "$LOGDIR/$id.log"; then
    record PASS "$label via Nexus" "answered NEXUS-OK"
  elif grep -qi "not logged in\|login\|setup-token" "$LOGDIR/$id.log"; then
    record FAIL "$label via Nexus" "not logged in (see excerpt for the command to run)"
  elif grep -qi "limit" "$LOGDIR/$id.log"; then
    record FAIL "$label via Nexus" "usage limit reached"
  else
    record FAIL "$label via Nexus" "did not answer (see excerpt)"
  fi
}
step "Claude Code via Nexus (subscription)"
if [[ "$SKIP_SUBS" == 1 ]]; then record SKIP "Claude Code via Nexus" "--skip-subscriptions"
elif [[ "$HUB_UP" == 1 ]] && have claude; then sub_test claude-code selftest-claude "Claude Code"
else record SKIP "Claude Code via Nexus" "claude CLI missing or no hub"; fi

step "Codex via Nexus (subscription)"
if [[ "$SKIP_SUBS" == 1 ]]; then record SKIP "Codex via Nexus" "--skip-subscriptions"
elif [[ "$HUB_UP" == 1 ]] && have codex; then sub_test codex selftest-codex "Codex"
else record SKIP "Codex via Nexus" "codex CLI missing or no hub"; fi

# ---------------------------------------------------------------- 11-12. iOS
step "iPhone app compiles (Simulator, no signing)"
if [[ "$SKIP_IOS" == 1 ]]; then record SKIP "iOS app build" "--skip-ios"
elif have xcodebuild && have xcodegen; then
  if (cd "$REPO_ROOT/ios" && xcodegen generate >"$LOGDIR/xcodegen.log" 2>&1); then
    if (cd "$REPO_ROOT/ios" && xcodebuild -project Nexus.xcodeproj -scheme Nexus \
        -destination 'generic/platform=iOS Simulator' -derivedDataPath "$WORK/dd" \
        CODE_SIGNING_ALLOWED=NO build >"$LOGDIR/ios-build.log" 2>&1); then
      record PASS "iOS app build" "all targets compiled"
    else
      record FAIL "iOS app build" "$(grep -c ' error: ' "$LOGDIR/ios-build.log") compile errors (see excerpt)"
    fi
  else
    record FAIL "iOS app build" "xcodegen failed"
  fi
  step "NexusKit wire-format tests (Simulator)"
  sim="$(xcrun simctl list devices available 2>/dev/null | grep -E '^ +iPhone' | head -n1 | sed -E 's/.*\(([0-9A-F-]{36})\).*/\1/' || true)"
  if [[ -n "$sim" ]]; then
    if (cd "$REPO_ROOT/ios/NexusKit" && xcodebuild test -scheme NexusKit-Package -destination "id=$sim" \
        -derivedDataPath "$WORK/dd-kit" >"$LOGDIR/ios-test.log" 2>&1); then
      record PASS "NexusKit tests" "byte formats match the backend"
    else
      record FAIL "NexusKit tests" "tests failed or did not compile (see excerpt)"
    fi
  else
    record SKIP "NexusKit tests" "no iPhone Simulator installed (Xcode → Settings → Components)"
  fi
else
  record SKIP "iOS app build" "xcodebuild or xcodegen missing"
fi

# ---------------------------------------------------------------- report
{
  echo "Nexus self-test $STAMP — $(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null) on $(git -C "$REPO_ROOT" branch --show-current 2>/dev/null)"
  echo
  echo "== Summary =="
  printf '%s' "$RESULTS" | awk -F'|' 'NF>1{printf "%-5s %-24s %s\n", $1, $2, $3}'
  echo
  echo "== Tools =="; sed 's/^/    /' "$LOGDIR/tools.txt"
  for f in go-test web build run-local status echo shell runtimes llm selftest-claude selftest-codex xcodegen; do
    [[ -s "$LOGDIR/$f.log" ]] || continue
    if grep -qiE 'FAIL|error|panic|not logged|limit' "$LOGDIR/$f.log" || [[ "$f" == runtimes || "$f" == status ]]; then
      echo; echo "== $f (last lines) =="; excerpt "$LOGDIR/$f.log" 30
    fi
  done
  if [[ -s "$LOGDIR/ios-build.log" ]] && grep -q ' error: ' "$LOGDIR/ios-build.log"; then
    echo; echo "== iOS compile errors (first 120) =="
    grep ' error: ' "$LOGDIR/ios-build.log" | sed "s|$REPO_ROOT/||" | sort -u | head -n 120 | sed 's/^/    /'
  fi
  if [[ -s "$LOGDIR/ios-test.log" ]] && grep -qE ' error: |failed' "$LOGDIR/ios-test.log"; then
    echo; echo "== NexusKit test failures =="
    grep -E ' error: |XCTAssert|failed' "$LOGDIR/ios-test.log" | sed "s|$REPO_ROOT/||" | head -n 60 | sed 's/^/    /'
  fi
  echo
  echo "Full logs: $LOGDIR"
} >"$REPORT"

echo
cat "$REPORT"
echo
if [[ "$FAILS" == 0 ]]; then ok "All checks passed."; else warn "$FAILS check(s) failed. Paste $REPORT back into the chat."; fi
say "(Tip: pbcopy < \"$REPORT\" copies it to the clipboard.)"
exit 0
