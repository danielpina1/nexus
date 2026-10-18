#!/usr/bin/env bash
# Run a throw-away Hub + node in the foreground for development, using the
# MacBook profile (backend/configs/macbook) in a temporary directory.
#
#   scripts/mac/run-local.sh [--dir DIR] [--port 7701] [--bin DIR] [--with-ollama]   (make run-local)
#
# - builds backend/bin/ (nexusd, nexus-node, nexus); --bin DIR uses prebuilt
#   binaries from DIR instead
# - creates DIR/hub (nexus.yaml + vault) and DIR/node (node.yaml), listening
#   on 127.0.0.1:PORT only (7701 by default, so it doesn't clash with the
#   launchd Hub on 7700)
# - starts the Hub, pairs the node with it (mTLS), starts the node and follows
#   both logs; Ctrl-C stops both
# Re-running with the same --dir (or NEXUS_LOCAL_DIR) reuses its data.
# Works on macOS and Linux.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

DIR="${NEXUS_LOCAL_DIR:-}"
BIN_OVERRIDE=""
PORT=7701
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --with-ollama) export NEXUS_WITH_OLLAMA=1; shift ;;
    --bin) BIN_OVERRIDE="$2"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $1" ;;
  esac
done
[[ "$PORT" =~ ^[0-9]+$ ]] || die "--port: not a number"
if [[ -z "$DIR" ]]; then DIR="$(mktemp -d "${TMPDIR:-/tmp}/nexus-local.XXXXXX")"; fi
mkdir -p "$DIR"
DIR="$(cd "$DIR" && pwd)"

# lib.sh helpers (hub_healthy, wait_for_hub) read these.
NEXUS_HUB_DIR="$DIR/hub"
NEXUS_NODE_DIR="$DIR/node"
NEXUS_PORT="$PORT"
BIN="${BIN_OVERRIDE:-$REPO_ROOT/backend/bin}"
URL="https://localhost:$PORT"
HUB_PID=""
NODE_PID=""
TAIL_PID=""

# shellcheck disable=SC2329  # invoked by the EXIT trap
stop_all() {
  local p
  if [[ -n "$TAIL_PID" ]]; then kill "$TAIL_PID" 2>/dev/null || true; fi
  for p in "$NODE_PID" "$HUB_PID"; do
    if [[ -n "$p" ]]; then kill "$p" 2>/dev/null || true; fi
  done
  for p in "$NODE_PID" "$HUB_PID"; do
    if [[ -n "$p" ]]; then wait "$p" 2>/dev/null || true; fi
  done
}
trap stop_all EXIT
trap 'exit 130' INT TERM

step "Build"
if [[ -n "$BIN_OVERRIDE" ]]; then
  info "using prebuilt binaries in $BIN"
else
  run_in "$REPO_ROOT/backend" go build -o bin/ ./cmd/...
fi
for b in nexusd nexus-node; do [[ -x "$BIN/$b" ]] || die "$BIN/$b not found"; done

step "Hub in $NEXUS_HUB_DIR"
mkdir -p "$NEXUS_HUB_DIR" "$NEXUS_NODE_DIR"
if [[ ! -f "$NEXUS_HUB_DIR/nexus.yaml" ]]; then
  web=""
  [[ -f "$REPO_ROOT/web/dist/index.html" ]] && web="$REPO_ROOT/web/dist"
  token="$(render_hub_config "$NEXUS_HUB_DIR/nexus.yaml" "$(local_host_name)" "$web")"
  # Loopback only, on the dev port.
  sed -e "s|listen: \"0.0.0.0:7700\"|listen: \"127.0.0.1:$PORT\"|" \
      -e "s|:7700\"|:$PORT\"|g" "$NEXUS_HUB_DIR/nexus.yaml" > "$NEXUS_HUB_DIR/nexus.yaml.tmp"
  mv "$NEXUS_HUB_DIR/nexus.yaml.tmp" "$NEXUS_HUB_DIR/nexus.yaml"
  chmod 600 "$NEXUS_HUB_DIR/nexus.yaml"
  ok "wrote nexus.yaml (dev token $token)"
fi
if [[ ! -d "$NEXUS_HUB_DIR/vault" ]]; then
  run "$BIN/nexusd" init -dir "$NEXUS_HUB_DIR" -template "$REPO_ROOT/vault-template" \
    -hub-url "$URL" -listen "127.0.0.1:$PORT" >/dev/null
  ok "vault created"
fi
if [[ ! -f "$NEXUS_NODE_DIR/node.yaml" ]]; then
  sed -e "s|hub_url: https://localhost:7700|hub_url: $URL|" \
    "$REPO_ROOT/backend/configs/macbook/node.yaml" > "$NEXUS_NODE_DIR/node.yaml"
  [[ "${NEXUS_WITH_OLLAMA:-0}" == 1 ]] && enable_ollama "$NEXUS_NODE_DIR/node.yaml"
fi

(cd "$NEXUS_HUB_DIR" && exec "$BIN/nexusd" serve -config nexus.yaml) >>"$DIR/hub.log" 2>&1 &
HUB_PID=$!
wait_for_hub 45 || { tail -n 30 "$DIR/hub.log" >&2; die "Hub did not answer on $URL/healthz"; }
ok "Hub up on $URL (pid $HUB_PID)"

step "Node in $NEXUS_NODE_DIR"
if [[ ! -f "$NEXUS_NODE_DIR/data/node/node.token" ]]; then
  pair_local_node "$BIN/nexusd" "$BIN/nexus-node" "$NEXUS_HUB_DIR" "$NEXUS_NODE_DIR" "$NEXUS_NODE_ID" "$URL" >>"$DIR/node.log" 2>&1
  ok "node \"$NEXUS_NODE_ID\" paired"
fi
(cd "$NEXUS_NODE_DIR" && exec "$BIN/nexus-node" run -config node.yaml) >>"$DIR/node.log" 2>&1 &
NODE_PID=$!
ok "node running (pid $NODE_PID)"

tok="$(awk '/^  dev_token:/{print $2; exit}' "$NEXUS_HUB_DIR/nexus.yaml")"
cat <<EOF

${C_BOLD}Nexus (dev) is running${C_RESET} — data in $DIR
  Dashboard:  $URL   (dev token: $tok)
  CLI:        export NEXUS_URL=$URL NEXUS_CA_FILE=$(hub_ca) NEXUS_TOKEN=$tok
              $BIN/nexus run "hello" -agent echo -follow
  Logs:       $DIR/hub.log, $DIR/node.log (following below; Ctrl-C stops both)

EOF
tail -n +1 -F "$DIR/hub.log" "$DIR/node.log" &
TAIL_PID=$!
# Exit when either daemon dies (or on Ctrl-C).
while kill -0 "$HUB_PID" 2>/dev/null && kill -0 "$NODE_PID" 2>/dev/null; do sleep 1; done
kill "$TAIL_PID" 2>/dev/null || true
warn "a daemon exited — see the logs above"
exit 1
