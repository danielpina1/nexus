#!/usr/bin/env bash
# Build the Nexus iPhone app on this Mac (docs/SETUP_MAC.md §6, ios/README.md).
#
#   scripts/mac/ios-build.sh [--team ABCDE12345] [--bundle-prefix com.yourname]
#                            [--free-team] [--reconfigure] [--skip-tests] [--no-open] [--dry-run] [--yes]
#
#   --free-team  sign with a free Apple ID (Personal Team): removes the push
#                notification entitlements locally (no lock-screen approvals
#                or Live Activity push; reinstall every 7 days). Undo with
#                git checkout -- 'ios/*.entitlements' 'ios/*/*.entitlements'
#
# 1. checks Xcode 16.3+ and XcodeGen
# 2. writes ios/Config/Local.xcconfig (git-ignored): your Team ID, bundle id and
#    App Group, asking for them the first time (--reconfigure to change)
# 3. checks the wire formats: Go vectors (backend) + NexusKit tests on an iOS
#    Simulator (CanonicalJSON, signatures, pairing proof, safety code)
# 4. runs `xcodegen generate` and opens ios/Nexus.xcodeproj
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TEAM=""
PREFIX=""
RECONFIGURE=0
SKIP_TESTS=0
NO_OPEN=0
FREE_TEAM=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --team) TEAM="$2"; shift 2 ;;
    --bundle-prefix) PREFIX="$2"; shift 2 ;;
    --reconfigure) RECONFIGURE=1; shift ;;
    --skip-tests) SKIP_TESTS=1; shift ;;
    --no-open) NO_OPEN=1; shift ;;
    --free-team) FREE_TEAM=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    -h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $1 (see --help)" ;;
  esac
done

IOS="$REPO_ROOT/ios"
LOCAL_XC="$IOS/Config/Local.xcconfig"

# strip_push_entitlements — a free Personal Team cannot sign push or
# time-sensitive / communication notification entitlements.
strip_push_entitlements() {
  local f k
  for f in "$IOS"/*/*.entitlements; do
    for k in aps-environment com.apple.developer.usernotifications.time-sensitive com.apple.developer.usernotifications.communication; do
      if [[ "$DRY_RUN" == 1 ]]; then
        say "  + PlistBuddy -c 'Delete :$k' ${f#"$REPO_ROOT"/}"
      else
        /usr/libexec/PlistBuddy -c "Delete :$k" "$f" >/dev/null 2>&1 || true
      fi
    done
  done
  ok "push entitlements removed for a free Personal Team (local change; see --help to undo)"
}

# -----------------------------------------------------------------------------
step "Xcode and XcodeGen"
require_macos_or_dry_run
load_brew_env
if have xcodebuild && xcodebuild -version >/dev/null 2>&1; then
  xv="$(xcodebuild -version | awk 'NR==1{print $2}')"
  major="${xv%%.*}"; rest="${xv#*.}"; minor="${rest%%.*}"
  [[ "$minor" =~ ^[0-9]+$ ]] || minor=0
  if (( major > 16 || (major == 16 && minor >= 3) )); then ok "Xcode $xv ($(xcode-select -p))"
  else die "Xcode $xv is too old: the generated Swift needs Xcode 16.3+ (Swift 6.1). Update it in the App Store."; fi
else
  err "Xcode is not installed or not selected."
  say "  Install Xcode from the Mac App Store, open it once (installs the iOS platform), then:"
  hint "sudo xcode-select -s /Applications/Xcode.app/Contents/Developer"
  hint "sudo xcodebuild -license accept"
  [[ "$DRY_RUN" == 1 ]] || exit 1
fi
if have xcodegen; then ok "xcodegen $(xcodegen --version 2>/dev/null | awk '{print $NF}')"
elif confirm "XcodeGen is missing. Install it (brew install xcodegen)?"; then run brew install xcodegen
else die "XcodeGen is required: brew install xcodegen"; fi

# -----------------------------------------------------------------------------
step "Signing: Team ID, bundle id, App Group → ios/Config/Local.xcconfig"
cat <<'EOF'
  Free Apple ID ("Personal Team") vs paid Apple Developer Program (99 USD/year):
   - Simulator: no account needed at all.
   - Free Personal Team on your iPhone: the app runs, but its provisioning
     profile expires after 7 days (Run from Xcode again to renew) and the
     Push Notifications capability is not available: no approval
     notifications, no Live Activity push. The project's targets declare
     aps-environment (and time-sensitive / communication notifications), so
     Xcode refuses to sign them with a Personal Team until those keys are
     removed from the .entitlements files. Xcode lists any other capability
     your team cannot use (it may also refuse App Groups / Keychain sharing).
   - Paid Apple Developer Program: every capability above, TestFlight, and
     profiles valid for a year. Recommended once Nexus is part of your day.
  Face ID and the Secure Enclave work on a real device with either account.
EOF
if [[ -f "$LOCAL_XC" && "$RECONFIGURE" == 0 && -z "$TEAM$PREFIX" ]]; then
  ok "$LOCAL_XC exists (--reconfigure to change):"
  sed -n 's/^\([A-Z_]* = .*\)$/      \1/p' "$LOCAL_XC"
else
  if [[ -z "$TEAM" ]]; then
    detected=""
    if is_macos && have security; then
      # Team ID = the OU of your "Apple Development" signing certificate.
      detected="$(security find-certificate -c "Apple Development" -p 2>/dev/null |
        openssl x509 -noout -subject 2>/dev/null | sed -n 's/.*OU *= *\([A-Z0-9]\{10\}\).*/\1/p' | head -n1 || true)"
    fi
    say "  Team ID: Xcode → Settings → Accounts → your team, or developer.apple.com → Account → Membership."
    [[ -n "$detected" ]] && info "detected from your signing certificate: $detected"
    TEAM="$(ask "Apple Team ID (10 characters, empty = Simulator only)" "$detected")"
  fi
  if [[ -n "$TEAM" && ! "$TEAM" =~ ^[A-Z0-9]{10}$ ]]; then die "Team ID '$TEAM' should be 10 upper-case letters/digits"; fi
  if [[ -z "$PREFIX" ]]; then
    def_prefix="com.$(id -un | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-')"
    PREFIX="$(ask "Bundle id prefix (reverse DNS you own or make up)" "$def_prefix")"
  fi
  [[ "$PREFIX" =~ ^[A-Za-z][A-Za-z0-9-]*(\.[A-Za-z0-9-]+)+$ ]] || die "bundle prefix '$PREFIX' should look like com.yourname"
  bundle="$PREFIX.nexus"
  body="// Local signing settings — written by scripts/mac/ios-build.sh. Git-ignored; never commit.
NEXUS_BUNDLE_ID = $bundle
NEXUS_APP_GROUP = group.$bundle
DEVELOPMENT_TEAM = $TEAM
NEXUS_APS_ENVIRONMENT = development
"
  if [[ "$DRY_RUN" == 1 ]]; then
    info "[dry-run] would write $LOCAL_XC:"; printf '%s' "$body" | sed 's/^/      /'
  else
    printf '%s' "$body" > "$LOCAL_XC"
    ok "wrote $LOCAL_XC (bundle $bundle, team ${TEAM:-none})"
  fi
  if have git && ! git -C "$REPO_ROOT" check-ignore -q "$LOCAL_XC"; then
    warn "$LOCAL_XC is not git-ignored — don't commit it"
  fi
  [[ -z "$TEAM" ]] && warn "No Team ID: build for the Simulator, or set the team later (--reconfigure)."
fi

# -----------------------------------------------------------------------------
step "Wire formats: Go ↔ Swift test vectors"
if [[ "$SKIP_TESTS" == 1 ]]; then
  info "--skip-tests"
else
  if have go || [[ "$DRY_RUN" == 1 ]]; then
    run_in "$REPO_ROOT/backend" go test ./internal/security/ -run 'TestCrossLanguageVectors' -count=1
  else
    warn "go not found; skipping the backend vectors"
  fi
  # NexusKit imports iOS-only frameworks (ActivityKit), so `swift test` on the
  # Mac itself cannot build it: run the package tests on an iOS Simulator.
  sim=""
  if have xcrun; then
    sim="$(xcrun simctl list devices available 2>/dev/null | grep -E '^ +iPhone' | head -n1 |
      sed -E 's/.*\(([0-9A-F-]{36})\).*/\1/' || true)"
  fi
  if [[ -n "$sim" || "$DRY_RUN" == 1 ]]; then
    run_in "$IOS/NexusKit" xcodebuild test -scheme NexusKit-Package -destination "id=${sim:-<simulator id>}" -quiet
    ok "NexusKit matches the backend byte formats"
  else
    warn "No iPhone Simulator found. Xcode → Settings → Components → install iOS, then re-run."
  fi
fi

# -----------------------------------------------------------------------------
step "Generate and open the Xcode project"
[[ "$FREE_TEAM" == 1 ]] && strip_push_entitlements
run_in "$IOS" xcodegen generate
if [[ "$NO_OPEN" == 0 ]]; then run open "$IOS/Nexus.xcodeproj"; fi
cat <<EOF

In Xcode:
  1. Scheme "Nexus", destination your iPhone (USB or same Wi-Fi) or a Simulator.
  2. Product → Run. First time on the iPhone:
     - Settings → Privacy & Security → Developer Mode → On (restarts the phone)
     - Personal Team only: Settings → General → VPN & Device Management → trust your Apple ID
  3. In the app: pair with the Hub on the home Wi-Fi —
     on the Mac: nexusd pair -config ~/Nexus/nexus.yaml  (or dashboard → Settings → Pair a device)
EOF
