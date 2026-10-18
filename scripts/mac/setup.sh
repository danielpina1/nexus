#!/usr/bin/env bash
# Nexus — "Day 1 on your MacBook" setup (docs/SETUP_MAC.md).
#
# Sets up everything on this Mac: the Hub (nexusd), a node (nexus-node),
# Ollama with local models, Chrome for the shopping agent, the Claude Code /
# Codex CLIs (your subscriptions), launchd agents, and the dashboard.
#
#   scripts/mac/setup.sh [--dry-run] [--yes] [--with-ollama] [--skip-models] [--skip-services]
#
#   --dry-run        print every step and command, change nothing
#   --yes            answer yes to every question (installs, model pulls…)
#   --with-ollama    also set up local models in Ollama (default: Claude Code
#                    and Codex subscriptions only; no local model runs)
#   --skip-models    don't pull Ollama models (with --with-ollama)
#   --skip-services  don't install launchd agents (run the Hub yourself)
#
# Safe to re-run: existing configs, vault, pairing and binaries are kept
# (binaries and the dashboard are rebuilt). It never uses sudo and never
# pipes a download into a shell; when something must be installed by hand it
# prints the command. Paths: NEXUS_HUB_DIR (~/Nexus), NEXUS_NODE_DIR
# (~/Nexus/node), NEXUS_BIN_DIR (~/.nexus/bin).
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SKIP_MODELS=0
SKIP_SERVICES=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --yes|-y) ASSUME_YES=1 ;;
    --skip-models) SKIP_MODELS=1 ;;
    --with-ollama) export NEXUS_WITH_OLLAMA=1 ;;
    --skip-services) SKIP_SERVICES=1 ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown flag $a (see --help)" ;;
  esac
done

HOST="$(local_host_name)"
LOCAL_URL="https://localhost:$NEXUS_PORT"
BONJOUR_URL="https://$HOST.local:$NEXUS_PORT"
HOME_URL="https://nexus.home.arpa:$NEXUS_PORT"
TEMP_HUB_PID=""
MISSING_REQUIRED=0

cleanup() {
  if [[ -n "$TEMP_HUB_PID" ]]; then
    kill "$TEMP_HUB_PID" 2>/dev/null || true
    wait "$TEMP_HUB_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

say "${C_BOLD}Nexus setup for macOS${C_RESET}"
say "Repo: $REPO_ROOT"
say "Hub:  $NEXUS_HUB_DIR   Node: $NEXUS_NODE_DIR   Binaries: $NEXUS_BIN_DIR"
[[ "$DRY_RUN" == 1 ]] && say "${C_YELLOW}Dry run: nothing will be changed.${C_RESET}"

# -----------------------------------------------------------------------------
step "Check this Mac"
require_macos_or_dry_run
if is_macos; then
  ok "macOS $(sw_vers -productVersion) ($(uname -m)), host name $HOST.local"
  if [[ "$(uname -m)" != arm64 ]]; then
    if [[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" == 1 ]]; then
      warn "This shell runs under Rosetta (x86_64). Open a native Terminal so Homebrew and Go build arm64 binaries."
    else
      warn "Intel Mac: everything works, but local models in Ollama are much slower than on Apple Silicon."
    fi
  fi
  mem_gb=$(( $(sysctl -n hw.memsize) / 1073741824 ))
  info "memory: ${mem_gb} GB"
else
  mem_gb=16
  info "assuming an Apple Silicon Mac with ${mem_gb} GB for the plan"
fi

# -----------------------------------------------------------------------------
step "Homebrew"
load_brew_env
if have brew; then
  ok "Homebrew $(brew --version | head -n1 | awk '{print $2}') at $(command -v brew)"
else
  err "Homebrew is not installed. It is the package manager used for everything below."
  say "  Install it from https://brew.sh (check the command there), currently:"
  # shellcheck disable=SC2016  # printed literally for the user
  hint '/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
  say "  then follow its 'Next steps' (adds /opt/homebrew/bin to your PATH) and re-run this script."
  [[ "$DRY_RUN" == 1 ]] || exit 1
fi

# -----------------------------------------------------------------------------
TOOLS="go node buf xcodegen"
[[ "${NEXUS_WITH_OLLAMA:-0}" == 1 ]] && TOOLS="go node buf ollama xcodegen"
step "Command-line tools ($TOOLS)"
info "go: builds the Hub and node · node: builds the dashboard · buf: regenerates API code (make gen)"
info "xcodegen: generates the iOS Xcode project${NEXUS_WITH_OLLAMA:+ · ollama: local models}"
missing=()
for f in $TOOLS; do
  if have "$f"; then ok "$f: $(command -v "$f")"; else missing+=("$f"); fi
done
if [[ ${#missing[@]} -gt 0 ]]; then
  say "Missing: ${missing[*]}"
  if have brew || [[ "$DRY_RUN" == 1 ]]; then
    if confirm "Install them with Homebrew (brew install ${missing[*]})?"; then
      run brew install "${missing[@]}"
    else
      warn "Skipped. Install later: brew install ${missing[*]}"
    fi
  fi
fi
if have go; then
  gov="$(go env GOVERSION 2>/dev/null || true)"
  info "go version: ${gov:-unknown} (backend/go.mod needs Go 1.24+)"
fi
have go || [[ "$DRY_RUN" == 1 ]] || { err "go is required to build Nexus"; MISSING_REQUIRED=1; }
have npm || [[ "$DRY_RUN" == 1 ]] || { err "node/npm is required to build the dashboard"; MISSING_REQUIRED=1; }
[[ "$MISSING_REQUIRED" == 0 ]] || die "install the missing tools and re-run"

# -----------------------------------------------------------------------------
step "Google Chrome (shopping and browser agents)"
CHROME_APP="/Applications/Google Chrome.app"
if [[ -d "$CHROME_APP" || -d "$HOME/Applications/Google Chrome.app" ]]; then
  ok "Chrome found"
else
  say "The browser runtime drives Chrome (one profile per store, see docs/SHOPPING.md)."
  if confirm "Install Google Chrome with Homebrew (brew install --cask google-chrome)?"; then
    run brew install --cask google-chrome
  else
    warn "Skipped. Browser agents need Chrome or Chromium; install it later."
  fi
fi

# -----------------------------------------------------------------------------
step "Xcode (for the iPhone app — not needed by the Hub)"
xcode_dir="$(xcode-select -p 2>/dev/null || true)"
if [[ -d /Applications/Xcode.app ]] && [[ "$xcode_dir" == *Xcode*.app/* ]]; then
  ok "$(xcodebuild -version 2>/dev/null | head -n1 || echo Xcode) at $xcode_dir"
elif [[ -d /Applications/Xcode.app ]]; then
  warn "Xcode is installed but the active developer directory is ${xcode_dir:-unset}. Select it (asks for your password):"
  hint "sudo xcode-select -s /Applications/Xcode.app/Contents/Developer && sudo xcodebuild -license accept"
else
  warn "Xcode is not installed. Install it from the Mac App Store (search \"Xcode\", ~10 GB+),"
  say "  open it once to install the iOS platform, then run: scripts/mac/ios-build.sh"
  [[ -n "$xcode_dir" ]] && info "(found only the Command Line Tools at $xcode_dir — fine for building the Hub)"
fi

# -----------------------------------------------------------------------------
step "Claude Code and Codex CLIs (your subscriptions)"
say "Nexus runs your Claude and ChatGPT subscriptions through the official CLIs on this node."
if have claude; then
  ok "claude $(claude --version 2>/dev/null | head -n1 || echo '(version unknown)') at $(command -v claude)"
else
  warn "Claude Code is not installed. Use the official install method (check https://docs.claude.com/en/docs/claude-code for the current one):"
  hint "curl -fsSL https://claude.ai/install.sh | bash      # native installer → ~/.local/bin/claude"
  hint "npm install -g @anthropic-ai/claude-code             # or via npm"
fi
if have codex; then
  ok "codex $(codex --version 2>/dev/null | head -n1 || echo '(version unknown)') at $(command -v codex)"
  if codex login status >/dev/null 2>&1; then ok "codex is logged in"; else info "codex login status unknown — run: codex login"; fi
else
  warn "Codex CLI is not installed. Official options (check https://github.com/openai/codex for the current one):"
  hint "npm install -g @openai/codex"
  hint "brew install --cask codex"
fi
say "Log in once in a normal Terminal window (the node reuses the login):"
hint "claude          # then /login → your Claude subscription (Pro/Max)"
hint "codex login     # Sign in with ChatGPT (Plus/Pro)"
if [[ -n "${ANTHROPIC_API_KEY:-}" || -n "${OPENAI_API_KEY:-}" ]]; then
  warn "ANTHROPIC_API_KEY / OPENAI_API_KEY is set in this shell: the CLIs would bill the API instead of your subscription."
  warn "Remove it from your shell profile if you want subscription usage (the launchd agents don't inherit it)."
fi

# -----------------------------------------------------------------------------
step "Build Nexus (binaries → $NEXUS_BIN_DIR, dashboard → web/dist)"
version="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
if confirm "Build the Go binaries and the web dashboard now (a few minutes the first time)?"; then
  run mkdir -p "$NEXUS_BIN_DIR"
  run_in "$REPO_ROOT/backend" go build -ldflags "-X main.version=$version" -o "$NEXUS_BIN_DIR/" ./cmd/...
  run_in "$REPO_ROOT/backend" go build -o "$NEXUS_BIN_DIR/nexus-browser-login" ./internal/runtime/browser/cmd/nexus-browser-login
  run_in "$REPO_ROOT/web" npm ci --no-audit --no-fund
  run_in "$REPO_ROOT/web" npm run build
  ok "built nexusd, nexus-node, nexus, nexus-browser-login ($version) and web/dist"
else
  warn "Skipped the build; later steps need $NEXUS_BIN_DIR/nexusd and nexus-node."
fi
case ":$PATH:" in
  *":$NEXUS_BIN_DIR:"*) ;;
  *) info "Tip: add the binaries to your PATH (~/.zshrc): export PATH=\"$NEXUS_BIN_DIR:\$PATH\"" ;;
esac
NEXUSD="$NEXUS_BIN_DIR/nexusd"
NODE_BIN="$NEXUS_BIN_DIR/nexus-node"

# -----------------------------------------------------------------------------
step "Create the Hub in $NEXUS_HUB_DIR"
HUB_CFG="$NEXUS_HUB_DIR/nexus.yaml"
if [[ -f "$HUB_CFG" ]]; then
  ok "$HUB_CFG exists — keeping it (delete it to start over from backend/configs/macbook/nexus.yaml)"
else
  say "Writes nexus.yaml from the MacBook profile with a fresh dev token and these Hub URLs:"
  info "$HOME_URL  (UniFi local DNS, docs/NETWORK.md)"
  info "$BONJOUR_URL  (this Mac on the home Wi-Fi)"
  run mkdir -p "$NEXUS_HUB_DIR"
  if [[ "$DRY_RUN" == 1 ]]; then
    info "[dry-run] would render backend/configs/macbook/nexus.yaml → $HUB_CFG (dev token, $HOST.local, web_dir)"
  else
    token="$(render_hub_config "$HUB_CFG" "$HOST" "$REPO_ROOT/web/dist")"
    ok "wrote $HUB_CFG (mode 600). Dev token: $token"
    info "(it stays in nexus.yaml: grep dev_token $HUB_CFG)"
  fi
fi
# nexusd init keeps the existing nexus.yaml and copies the starter vault.
if [[ -d "$NEXUS_HUB_DIR/vault" ]]; then
  ok "vault exists: $NEXUS_HUB_DIR/vault"
else
  run "$NEXUSD" init -dir "$NEXUS_HUB_DIR" -template "$REPO_ROOT/vault-template" \
    -hub-url "$HOME_URL" -hub-url "$BONJOUR_URL" -listen "0.0.0.0:$NEXUS_PORT"
fi

VAULT="$NEXUS_HUB_DIR/vault"
if [[ -d "$VAULT/.git" ]]; then
  ok "vault is a git repository (Nexus auto-commits changes)"
elif have git; then
  say "Nexus versions the vault with git (auto-commit, history, optional remote for Obsidian elsewhere)."
  if confirm "Make $VAULT a git repository?"; then
    git_id=()
    git config user.email >/dev/null 2>&1 || git_id=(-c user.name=Nexus -c user.email=nexus@localhost)
    run git -C "$VAULT" init -q
    run git -C "$VAULT" add -A
    run git ${git_id[@]+"${git_id[@]}"} -C "$VAULT" commit -q -m "Nexus vault from vault-template"
  fi
fi

# -----------------------------------------------------------------------------
step "Configure the MacBook node in $NEXUS_NODE_DIR"
NODE_CFG="$NEXUS_NODE_DIR/node.yaml"
if [[ -f "$NODE_CFG" ]]; then
  ok "$NODE_CFG exists — keeping it"
else
  run mkdir -p "$NEXUS_NODE_DIR"
  run cp "$REPO_ROOT/backend/configs/macbook/node.yaml" "$NODE_CFG"
  [[ "${NEXUS_WITH_OLLAMA:-0}" == 1 && "$DRY_RUN" != 1 ]] && enable_ollama "$NODE_CFG"
  run chmod 600 "$NODE_CFG"
  info "node id \"$NEXUS_NODE_ID\", tags [mac, dev, browser] — edit node.tags to add always-on/gpu (see the comments)"
fi

# -----------------------------------------------------------------------------
step "Ollama and local models"
if [[ "${NEXUS_WITH_OLLAMA:-0}" != 1 ]]; then
  ok "skipped: subscriptions only (Claude Code + Codex); no local models (add --with-ollama to use them)"
elif curl -fsS --max-time 2 http://localhost:11434/api/version >/dev/null 2>&1; then
  ok "Ollama is running on localhost:11434"
else
  say "Ollama is not running. Homebrew can run it as a background service (starts at login)."
  if confirm "Start it now with: brew services start ollama?"; then
    run brew services start ollama
    if [[ "$DRY_RUN" != 1 ]]; then
      for _ in 1 2 3 4 5 6 7 8 9 10; do
        curl -fsS --max-time 2 http://localhost:11434/api/version >/dev/null 2>&1 && break
        sleep 1
      done
    fi
  else
    warn "Skipped. Start Ollama yourself (the Ollama app or: ollama serve)."
  fi
fi
if [[ "${NEXUS_WITH_OLLAMA:-0}" != 1 ]]; then
  :
elif [[ "$SKIP_MODELS" == 1 ]]; then
  info "--skip-models: not pulling models"
else
  # alias  model  approx. download size  why
  models="fast-local|qwen2.5:7b|4.7 GB|router classifier, quick answers (tool calling + JSON)
embed|nomic-embed-text|0.3 GB|vault semantic search
smart|qwen2.5:14b|9.0 GB|better local agent model (recommended with 24 GB+ memory)"
  have_list="$(ollama list 2>/dev/null || true)"
  # fd 3, so the questions inside the loop still read from the terminal
  while IFS='|' read -r alias model size why <&3; do
    if printf '%s\n' "$have_list" | awk '{print $1}' | grep -qx -e "$model" -e "$model:latest"; then
      ok "$alias → $model already pulled"
      continue
    fi
    def=y
    if [[ "$alias" == smart && "$mem_gb" -lt 24 ]]; then def=n; fi
    if confirm "Pull $model for '$alias' ($size download — $why)?" "$def"; then
      run ollama pull "$model"
    else
      info "skipped $model (later: ollama pull $model)"
    fi
  done 3<<< "$models"
  info "fast-local and embed are pinned in nexus.yaml / node.yaml; smart/best pick the best pulled model automatically."
fi

# -----------------------------------------------------------------------------
step "Pair the MacBook node with the Hub (mTLS)"
if [[ -f "$NEXUS_NODE_DIR/data/node/node.token" ]]; then
  ok "node already paired ($NEXUS_NODE_DIR/data/node) — to re-pair, revoke it and delete that folder"
else
  # The Hub must be running: use the launchd agent if it is up, else a
  # temporary Hub just for pairing.
  if [[ "$DRY_RUN" != 1 ]] && ! hub_healthy; then
    say "Starting a temporary Hub for pairing (log: $NEXUS_LOG_DIR/setup-hub.log)…"
    mkdir -p "$NEXUS_LOG_DIR"
    (cd "$NEXUS_HUB_DIR" && exec "$NEXUSD" serve -config nexus.yaml) >>"$NEXUS_LOG_DIR/setup-hub.log" 2>&1 &
    TEMP_HUB_PID=$!
    wait_for_hub 45 || die "the Hub did not answer on $LOCAL_URL/healthz — see $NEXUS_LOG_DIR/setup-hub.log"
  fi
  if [[ "$DRY_RUN" == 1 ]]; then
    run_in "$NEXUS_HUB_DIR" "$NEXUSD" pair -kind node -label "MacBook node (setup.sh)" -hub-url "$LOCAL_URL" -config nexus.yaml
    run_in "$NEXUS_NODE_DIR" "$NODE_BIN" pair -config node.yaml -id "$NEXUS_NODE_ID" "<nexus://pair URL>"
  else
    pair_local_node "$NEXUSD" "$NODE_BIN" "$NEXUS_HUB_DIR" "$NEXUS_NODE_DIR" "$NEXUS_NODE_ID" "$LOCAL_URL"
    ok "node \"$NEXUS_NODE_ID\" paired (same machine, so the safety codes match by construction)"
  fi
  cleanup; TEMP_HUB_PID=""
fi

# -----------------------------------------------------------------------------
step "Check models and runtimes"
if [[ "$DRY_RUN" == 1 ]]; then
  if [[ "${NEXUS_WITH_OLLAMA:-0}" == 1 ]]; then
    run_in "$NEXUS_HUB_DIR" "$NEXUSD" llm check -config nexus.yaml -quick
    run_in "$NEXUS_NODE_DIR" "$NODE_BIN" llm check -config node.yaml -quick
  fi
  run_in "$NEXUS_NODE_DIR" "$NODE_BIN" runtimes -config node.yaml
elif [[ "${NEXUS_WITH_OLLAMA:-0}" != 1 ]]; then
  info "no local models (subscriptions only): the claude-code and codex runtimes below must be ready"
  run_in "$NEXUS_NODE_DIR" "$NODE_BIN" runtimes -config node.yaml || true
else
  run_in "$NEXUS_HUB_DIR" "$NEXUSD" llm check -config nexus.yaml -quick || warn "hub model check reported problems (details above; docs/MODELS.md §5)"
  run_in "$NEXUS_NODE_DIR" "$NODE_BIN" llm check -config node.yaml -quick || warn "node model check reported problems"
  run_in "$NEXUS_NODE_DIR" "$NODE_BIN" runtimes -config node.yaml || true
  info "Full probes (tool calling, JSON, streaming): nexusd llm check -config $HUB_CFG"
fi

# -----------------------------------------------------------------------------
step "Run Nexus in the background (launchd)"
if [[ "$SKIP_SERVICES" == 1 ]]; then
  info "--skip-services. Run in the foreground instead:"
  hint "cd $NEXUS_HUB_DIR && $NEXUSD serve"
  hint "cd $NEXUS_NODE_DIR && $NODE_BIN run"
else
  say "Installs two LaunchAgents (start at login, restart on crash; logs in $NEXUS_LOG_DIR):"
  info "com.nexus.hub → nexusd serve · com.nexus.node → nexus-node run"
  if confirm "Install and start them?"; then
    svc_flags=(--yes)
    [[ "$DRY_RUN" == 1 ]] && svc_flags+=(--dry-run)
    "$REPO_ROOT/scripts/mac/service.sh" "${svc_flags[@]}" install all
    if [[ "$DRY_RUN" != 1 ]]; then
      if wait_for_hub 30; then ok "Hub is up: $LOCAL_URL"; else warn "Hub not answering yet — scripts/mac/service.sh logs hub"; fi
    fi
  else
    info "Later: scripts/mac/service.sh install   (or make mac-services)"
  fi
fi

# -----------------------------------------------------------------------------
step "Next steps"
cat <<EOF
${C_BOLD}Dashboard${C_RESET}
  Open $LOCAL_URL (or $BONJOUR_URL from another device on Wi-Fi).
  The certificate comes from your Hub's private CA, so the browser warns once:
  continue to the site, then paste the dev token in Settings
  (grep dev_token $HUB_CFG).

${C_BOLD}iPhone${C_RESET}
  1. Build and install the app:  scripts/mac/ios-build.sh   (make ios)
  2. On the home Wi-Fi:           $NEXUSD pair -config $HUB_CFG
     (or dashboard → Settings → Pair a device) and scan the QR in the app.
  3. Compare the 6-emoji safety code on both screens.

${C_BOLD}Shops${C_RESET} (once per store, with no shopping run active):
  $NEXUS_BIN_DIR/nexus-browser-login -data-dir $NEXUS_NODE_DIR/data -profile worten -url https://www.worten.pt/

${C_BOLD}Remote access${C_RESET} — UniFi checklist (docs/NETWORK.md):
  [ ] Fixed IP for this Mac (UniFi → Client → Fixed IP)
  [ ] Local DNS record: nexus.home.arpa → that IP
  [ ] WireGuard VPN server + iPhone client with On-Demand (or Teleport)
  [ ] Firewall: allow TCP $NEXUS_PORT to this Mac from home LAN and VPN clients
  [ ] Pair the iPhone on home Wi-Fi; test once on cellular with the VPN on

${C_BOLD}Remember${C_RESET}: when the MacBook sleeps (lid closed) the Hub is offline and
the iPhone shows "Hub unreachable". See docs/SETUP_MAC.md §9 (sleep) and §11
(moving the Hub to an always-on machine).

Services: scripts/mac/service.sh status | logs | restart
EOF
