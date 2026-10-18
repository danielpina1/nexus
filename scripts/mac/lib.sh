# shellcheck shell=bash
# shellcheck disable=SC2034  # variables are used by the scripts that source this file
# Shared helpers for scripts/mac/*.sh. Sourced, not executed.
#
# Compatible with the bash 3.2 that ships with macOS: no associative arrays,
# no ${var,,}, no mapfile, empty arrays expanded with ${a[@]+"${a[@]}"}.

# --- Paths (override with environment variables) ------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NEXUS_HUB_DIR="${NEXUS_HUB_DIR:-$HOME/Nexus}"            # nexus.yaml, vault/, data/
NEXUS_NODE_DIR="${NEXUS_NODE_DIR:-$NEXUS_HUB_DIR/node}"  # node.yaml, data/
NEXUS_BIN_DIR="${NEXUS_BIN_DIR:-$HOME/.nexus/bin}"       # nexusd, nexus-node, nexus, nexus-browser-login
NEXUS_LOG_DIR="${NEXUS_LOG_DIR:-$HOME/Library/Logs/Nexus}"
NEXUS_PORT="${NEXUS_PORT:-7700}"
NEXUS_NODE_ID="${NEXUS_NODE_ID:-macbook}"
LAUNCH_AGENTS_DIR="${LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
HUB_LABEL="com.nexus.hub"
NODE_LABEL="com.nexus.node"
export REPO_ROOT NEXUS_HUB_DIR NEXUS_NODE_DIR NEXUS_BIN_DIR NEXUS_LOG_DIR NEXUS_PORT NEXUS_NODE_ID

# --- Flags shared by every script ---------------------------------------------
DRY_RUN="${DRY_RUN:-0}"
ASSUME_YES="${ASSUME_YES:-0}"

# --- Output --------------------------------------------------------------------
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'; C_RED=$'\033[31m'; C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'; C_RESET=$'\033[0m'
else
  C_BOLD=""; C_DIM=""; C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""; C_RESET=""
fi

STEP_NO=0
step() { STEP_NO=$((STEP_NO + 1)); printf '\n%s== %d. %s ==%s\n' "$C_BOLD$C_BLUE" "$STEP_NO" "$*" "$C_RESET"; }
say()  { printf '%s\n' "$*"; }
info() { printf '%s  %s%s\n' "$C_DIM" "$*" "$C_RESET"; }
ok()   { printf '%s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
err()  { printf '%s✗%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
die()  { err "$*"; exit 1; }
hint() { printf '    %s$ %s%s\n' "$C_BOLD" "$*" "$C_RESET"; }

# run CMD ARGS… — execute, or only print it with --dry-run.
run() {
  if [[ "$DRY_RUN" == 1 ]]; then
    printf '%s  [dry-run] would run:%s %s\n' "$C_YELLOW" "$C_RESET" "$(quote_cmd "$@")"
    return 0
  fi
  printf '%s  + %s%s\n' "$C_DIM" "$(quote_cmd "$@")" "$C_RESET"
  "$@"
}

# run_in DIR CMD ARGS… — like run, inside DIR (subshell).
run_in() {
  local dir="$1"; shift
  if [[ "$DRY_RUN" == 1 ]]; then
    printf '%s  [dry-run] would run in %s:%s %s\n' "$C_YELLOW" "$dir" "$C_RESET" "$(quote_cmd "$@")"
    return 0
  fi
  printf '%s  + (cd %s && %s)%s\n' "$C_DIM" "$dir" "$(quote_cmd "$@")" "$C_RESET"
  (cd "$dir" && "$@")
}

quote_cmd() {
  local out="" a
  for a in "$@"; do
    if [[ "$a" =~ ^[A-Za-z0-9_./:=@%+,-]+$ ]]; then out+="$a "; else out+="$(printf '%q' "$a") "; fi
  done
  printf '%s' "${out% }"
}

# confirm "Question?" [default y|n] — yes with --yes; in --dry-run the plan
# assumes yes (nothing is executed anyway); without a terminal it answers the
# default only if that is "n" (never install anything unasked).
confirm() {
  local q="$1" def="${2:-y}" ans prompt
  if [[ "$ASSUME_YES" == 1 ]]; then info "$q → yes (--yes)"; return 0; fi
  if [[ "$DRY_RUN" == 1 ]]; then info "$q → [dry-run: assuming yes]"; return 0; fi
  if [[ ! -t 0 ]]; then info "$q → no (not interactive; pass --yes)"; return 1; fi
  if [[ "$def" == y ]]; then prompt="[Y/n]"; else prompt="[y/N]"; fi
  read -r -p "${C_BOLD}?${C_RESET} $q $prompt " ans || return 1
  ans="$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]')"
  [[ -z "$ans" ]] && ans="$def"
  [[ "$ans" == y || "$ans" == yes ]]
}

# ask "Prompt" DEFAULT → prints the answer (DEFAULT when not interactive).
ask() {
  local q="$1" def="${2:-}" ans
  if [[ "$ASSUME_YES" == 1 || "$DRY_RUN" == 1 || ! -t 0 ]]; then printf '%s' "$def"; return 0; fi
  read -r -p "${C_BOLD}?${C_RESET} $q${def:+ [$def]} " ans || true
  printf '%s' "${ans:-$def}"
}

have() { command -v "$1" >/dev/null 2>&1; }

is_macos() { [[ "$(uname -s)" == Darwin ]]; }

# require_macos_or_dry_run SCRIPT — on another OS force --dry-run so the plan
# is still printed (used for testing on Linux).
require_macos_or_dry_run() {
  if ! is_macos; then
    warn "This is $(uname -s), not macOS: nothing will be changed, printing the plan only (--dry-run)."
    DRY_RUN=1
  fi
}

# Homebrew lives in /opt/homebrew on Apple Silicon, /usr/local on Intel.
load_brew_env() {
  if ! have brew; then
    local b
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do
      if [[ -x "$b" ]]; then eval "$("$b" shellenv)"; break; fi
    done
  fi
}

# local_host_name → Bonjour name without .local, lower-case (e.g. daniels-macbook-pro).
local_host_name() {
  local n=""
  if is_macos && have scutil; then n="$(scutil --get LocalHostName 2>/dev/null || true)"; fi
  [[ -z "$n" ]] && n="$(hostname -s 2>/dev/null || hostname)"
  printf '%s' "$n" | tr '[:upper:]' '[:lower:]'
}

# service_path → PATH for launchd agents: Nexus binaries, Homebrew,
# ~/.local/bin (native Claude Code installer), the system, plus the
# directories where claude / codex / node are found right now.
service_path() {
  local p="$NEXUS_BIN_DIR:/opt/homebrew/bin:/opt/homebrew/sbin:$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  local tool d
  for tool in claude codex node git; do
    if d="$(command -v "$tool" 2>/dev/null)" && [[ "$d" == /* ]]; then
      d="$(dirname "$d")"
      case ":$p:" in *":$d:"*) ;; *) p="$p:$d" ;; esac
    fi
  done
  printf '%s' "$p"
}

# sed_escape VALUE → safe as a sed replacement with | as delimiter.
sed_escape() { printf '%s' "$1" | sed -e 's/[\\|&]/\\&/g'; }

# lint_plist FILE — plutil on macOS, Python's plistlib elsewhere.
lint_plist() {
  if have plutil; then
    plutil -lint "$1" >/dev/null
  elif have python3; then
    python3 -I -c 'import plistlib,sys; plistlib.load(open(sys.argv[1],"rb"))' "$1"
  else
    warn "cannot validate $1 (no plutil or python3)"
  fi
}

# hub_ca → the Hub CA certificate (exists after the first nexusd start).
hub_ca() { printf '%s' "$NEXUS_HUB_DIR/data/security/pki/ca.crt"; }

# hub_healthy [URL] — GET /healthz with the Hub CA pinned.
hub_healthy() {
  local url="${1:-https://localhost:$NEXUS_PORT}" ca
  ca="$(hub_ca)"
  if [[ -f "$ca" ]]; then
    curl -fsS --max-time 3 --cacert "$ca" "$url/healthz" >/dev/null 2>&1
  else
    return 1
  fi
}

# wait_for_hub SECONDS
wait_for_hub() {
  local n="${1:-30}" i
  for ((i = 0; i < n; i++)); do
    hub_healthy && return 0
    sleep 1
  done
  return 1
}

# render_hub_config DST HOSTNAME WEB_DIR — write the MacBook profile
# (backend/configs/macbook/nexus.yaml) with a fresh random dev token, this
# Mac's Bonjour name and the dashboard path. Mode 600. Prints the token.
render_hub_config() {
  local dst="$1" host="$2" web="$3" token
  token="$(openssl rand -hex 24)"
  (
    umask 077
    sed -e "s|dev_token: CHANGE_ME|dev_token: $token|" \
        -e "s|my-macbook\\.local|$(sed_escape "$host").local|g" \
        -e "s|web_dir: \"\"|web_dir: \"$(sed_escape "$web")\"|" \
        "$REPO_ROOT/backend/configs/macbook/nexus.yaml" > "$dst"
  )
  grep -q "dev_token: $token" "$dst" || die "rendering $dst failed"
  [[ "${NEXUS_WITH_OLLAMA:-0}" == 1 ]] && enable_ollama "$dst"
  printf '%s' "$token"
}

# render_node_config DST — copy the MacBook node profile (Ollama block
# enabled only with NEXUS_WITH_OLLAMA=1).
render_node_config() {
  cp "$REPO_ROOT/backend/configs/macbook/node.yaml" "$1"
  [[ "${NEXUS_WITH_OLLAMA:-0}" == 1 ]] && enable_ollama "$1"
  return 0
}

# enable_ollama FILE — uncomment the "# >>> ollama … # <<< ollama" block of
# a MacBook profile and turn vault embeddings on (local models are opt-in).
enable_ollama() {
  local f="$1"
  sed -e '/^# >>> ollama/,/^# <<< ollama/ s/^#~ \{0,1\}//' \
      -e 's/embeddings: false # ollama-embeddings/embeddings: true/' "$f" > "$f.tmp" &&
    cat "$f.tmp" > "$f" && rm -f "$f.tmp"
}

# pair_local_node NEXUSD NODE_BIN HUB_DIR NODE_DIR NODE_ID HUB_URL — issue a
# node pairing on the Hub and redeem it with nexus-node (the Hub must be
# running). Writes the mTLS credentials into NODE_DIR/data/node and node.yaml.
pair_local_node() {
  local nexusd="$1" nodebin="$2" hubdir="$3" nodedir="$4" id="$5" url="$6" out pair_url
  out="$(cd "$hubdir" && "$nexusd" pair -kind node -label "$id node (scripts/mac)" -hub-url "$url" -config nexus.yaml)" ||
    die "nexusd pair failed"
  pair_url="$(printf '%s\n' "$out" | grep -o 'nexus://pair[^[:space:]]*' | head -n1 || true)"
  [[ -n "$pair_url" ]] || die "nexusd pair printed no nexus://pair URL"
  run_in "$nodedir" "$nodebin" pair -config node.yaml -id "$id" "$pair_url"
}
