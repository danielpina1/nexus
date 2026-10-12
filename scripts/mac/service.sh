#!/usr/bin/env bash
# Manage the Nexus launchd agents on macOS (per-user, gui/<uid> domain).
#
#   scripts/mac/service.sh install   [hub|node|all]   render deploy/macos/*.plist → ~/Library/LaunchAgents and load
#   scripts/mac/service.sh uninstall [hub|node|all]   unload and remove the plists (asks first)
#   scripts/mac/service.sh start     [hub|node|all]   load (if needed) and start
#   scripts/mac/service.sh stop      [hub|node|all]   unload until the next start / login
#   scripts/mac/service.sh restart   [hub|node|all]
#   scripts/mac/service.sh status    [hub|node|all]
#   scripts/mac/service.sh logs      [hub|node|all]   follow ~/Library/Logs/Nexus/*.log
#   scripts/mac/service.sh render    hub|node FILE    write a rendered plist (for checks)
#
# Flags: --dry-run (print what would happen), --yes (don't ask).
# Paths: NEXUS_HUB_DIR (~/Nexus), NEXUS_NODE_DIR (~/Nexus/node),
#        NEXUS_BIN_DIR (~/.nexus/bin), NEXUS_LOG_DIR (~/Library/Logs/Nexus).
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; }

ARGS=()
for a in "$@"; do
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --yes|-y) ASSUME_YES=1 ;;
    -h|--help|help) usage; exit 0 ;;
    *) ARGS+=("$a") ;;
  esac
done
set -- ${ARGS[@]+"${ARGS[@]}"}
CMD="${1:-}"
WHICH="${2:-all}"
[[ -z "$CMD" ]] && { usage; exit 2; }

DOMAIN="gui/$(id -u)"

label_of() { if [[ "$1" == hub ]]; then printf '%s' "$HUB_LABEL"; else printf '%s' "$NODE_LABEL"; fi; }
plist_of() { printf '%s/%s.plist' "$LAUNCH_AGENTS_DIR" "$(label_of "$1")"; }
log_of()   { printf '%s/%s.log' "$NEXUS_LOG_DIR" "$1"; }

targets() {
  case "$WHICH" in
    hub) echo hub ;;
    node) echo node ;;
    all) echo hub node ;;
    *) die "unknown service '$WHICH' (hub, node or all)" ;;
  esac
}

# render SVC OUT — fill the template's placeholders.
render() {
  local svc="$1" out="$2" tmpl
  tmpl="$REPO_ROOT/deploy/macos/$(label_of "$svc").plist"
  [[ -f "$tmpl" ]] || die "template $tmpl not found"
  sed -e "s|__BIN_DIR__|$(sed_escape "$NEXUS_BIN_DIR")|g" \
      -e "s|__HUB_DIR__|$(sed_escape "$NEXUS_HUB_DIR")|g" \
      -e "s|__NODE_DIR__|$(sed_escape "$NEXUS_NODE_DIR")|g" \
      -e "s|__LOG_DIR__|$(sed_escape "$NEXUS_LOG_DIR")|g" \
      -e "s|__HOME__|$(sed_escape "$HOME")|g" \
      -e "s|__PATH__|$(sed_escape "$(service_path)")|g" \
      "$tmpl" > "$out"
  if grep -q '__[A-Z_]*__' "$out"; then die "unfilled placeholder in $out"; fi
  lint_plist "$out"
}

loaded() { is_macos && launchctl print "$DOMAIN/$(label_of "$1")" >/dev/null 2>&1; }

check_prereqs() {
  local svc="$1" bin cfg
  if [[ "$svc" == hub ]]; then bin="$NEXUS_BIN_DIR/nexusd"; cfg="$NEXUS_HUB_DIR/nexus.yaml"
  else bin="$NEXUS_BIN_DIR/nexus-node"; cfg="$NEXUS_NODE_DIR/node.yaml"; fi
  [[ -x "$bin" ]] || warn "$bin is missing — build first (scripts/mac/setup.sh or make mac-setup)"
  [[ -f "$cfg" ]] || warn "$cfg is missing — run scripts/mac/setup.sh first"
}

do_install() {
  local svc="$1" plist tmp
  plist="$(plist_of "$svc")"
  check_prereqs "$svc"
  tmp="$(mktemp "${TMPDIR:-/tmp}/nexus-plist.XXXXXX")"
  render "$svc" "$tmp"
  if [[ "$DRY_RUN" == 1 ]]; then
    info "rendered $(label_of "$svc") (valid plist):"
    sed -n '/<key>ProgramArguments/,/<\/array>/p;/<key>PATH/,/<\/string>/p' "$tmp" | sed 's/^/      /'
  fi
  run mkdir -p "$LAUNCH_AGENTS_DIR" "$NEXUS_LOG_DIR"
  run install -m 0644 "$tmp" "$plist"
  rm -f "$tmp"
  if loaded "$svc"; then run launchctl bootout "$DOMAIN/$(label_of "$svc")" || true; fi
  run launchctl enable "$DOMAIN/$(label_of "$svc")"   # clears an earlier `launchctl disable`
  run launchctl bootstrap "$DOMAIN" "$plist"
  ok "$(label_of "$svc") installed → $plist (log: $(log_of "$svc"))"
}

do_uninstall() {
  local svc="$1" plist
  plist="$(plist_of "$svc")"
  confirm "Stop $(label_of "$svc") and remove $plist? (your data in $NEXUS_HUB_DIR stays)" n || return 0
  if loaded "$svc"; then run launchctl bootout "$DOMAIN/$(label_of "$svc")" || true; fi
  if [[ -f "$plist" || "$DRY_RUN" == 1 ]]; then run rm -f "$plist"; fi
  ok "$(label_of "$svc") removed"
}

do_start() {
  local svc="$1" plist
  plist="$(plist_of "$svc")"
  if loaded "$svc"; then
    run launchctl kickstart "$DOMAIN/$(label_of "$svc")"
  elif [[ -f "$plist" || "$DRY_RUN" == 1 ]]; then
    run launchctl bootstrap "$DOMAIN" "$plist"
  else
    die "$(label_of "$svc") is not installed: scripts/mac/service.sh install $svc"
  fi
  ok "$(label_of "$svc") started"
}

do_stop() {
  local svc="$1"
  if loaded "$svc" || [[ "$DRY_RUN" == 1 ]]; then
    # bootout unloads the agent (a plain kill would be restarted by KeepAlive
    # if the exit were unclean). It loads again at the next login or `start`.
    run launchctl bootout "$DOMAIN/$(label_of "$svc")" || true
    ok "$(label_of "$svc") stopped (starts again at next login, or: service.sh start $svc)"
  else
    info "$(label_of "$svc") is not running"
  fi
}

do_restart() {
  local svc="$1"
  if loaded "$svc"; then run launchctl kickstart -k "$DOMAIN/$(label_of "$svc")"; ok "$(label_of "$svc") restarted"
  else do_start "$svc"; fi
}

do_status() {
  local svc="$1" label out state pid code
  label="$(label_of "$svc")"
  if ! is_macos; then info "$label: launchctl not available on $(uname -s)"; return 0; fi
  if ! out="$(launchctl print "$DOMAIN/$label" 2>/dev/null)"; then
    if [[ -f "$(plist_of "$svc")" ]]; then warn "$label: installed but not loaded (service.sh start $svc)"
    else warn "$label: not installed (service.sh install $svc)"; fi
    return 0
  fi
  state="$(printf '%s\n' "$out" | awk -F'= ' '/^\tstate = /{print $2; exit}')"
  pid="$(printf '%s\n' "$out" | awk -F'= ' '/^\tpid = /{print $2; exit}')"
  code="$(printf '%s\n' "$out" | awk -F'= ' '/last exit code = /{print $2; exit}')"
  if [[ "$state" == running ]]; then ok "$label: running (pid ${pid:-?})"
  else warn "$label: ${state:-unknown} (last exit: ${code:-n/a}) — see service.sh logs $svc"; fi
  if [[ "$svc" == hub ]]; then
    if hub_healthy; then ok "hub answers on https://localhost:$NEXUS_PORT/healthz"
    else warn "hub does not answer on https://localhost:$NEXUS_PORT/healthz yet"; fi
  fi
}

do_logs() {
  local files=() svc
  for svc in $(targets); do files+=("$(log_of "$svc")"); done
  info "following ${files[*]} (Ctrl-C to stop)"
  if [[ "$DRY_RUN" == 1 ]]; then run tail -n 100 -F "${files[@]}"; return 0; fi
  exec tail -n 100 -F "${files[@]}"
}

case "$CMD" in
  render)
    [[ $# -eq 3 ]] || die "usage: service.sh render hub|node FILE"
    [[ "$2" == hub || "$2" == node ]] || die "render: hub or node"
    render "$2" "$3"; ok "wrote $3" ;;
  install|uninstall|start|stop|restart|status)
    if ! is_macos && [[ "$CMD" != status ]]; then
      warn "launchd is macOS only: printing the plan (--dry-run)."
      DRY_RUN=1
    fi
    for svc in $(targets); do "do_$CMD" "$svc"; done ;;
  logs) do_logs ;;
  *) usage; exit 2 ;;
esac
