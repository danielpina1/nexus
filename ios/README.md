# Nexus for iOS

SwiftUI app + widgets + Live Activities + notification extensions for the Nexus Hub.
Every target talks **only to the Hub API** (Connect protocol on `:7700`) and receives APNs pushes
that originate from the Hub — never directly to nodes, LLMs or other services.

## Requirements

- macOS with **Xcode 16.3+** (Swift 6.1 compiler; the generated protobuf code uses `nonisolated`
  type declarations). iOS 18 SDK. Targets use Swift language mode 5.
- **XcodeGen**: `brew install xcodegen`
- An **Apple Developer account** for anything beyond the Simulator: push notifications,
  App Groups, Keychain sharing, time-sensitive & communication notifications, Live Activity push.

## First build on your Mac

```sh
scripts/mac/ios-build.sh        # from the repo root, or: make ios
```

The script checks Xcode 16.3+ and XcodeGen, asks for your Team ID and a bundle id prefix and
writes `Config/Local.xcconfig`, runs the Go ↔ Swift wire-format vectors (backend test + the
`NexusKit-Package` tests on an iOS Simulator), then runs `xcodegen generate` and opens the
project. In Xcode pick the **Nexus** scheme and your iPhone (turn on Developer Mode on the phone
the first time) or a Simulator, and Run. A free Apple ID (Personal Team) can't sign the
Push Notifications capability this project declares and its profiles expire after 7 days; see
[docs/SETUP_MAC.md §6](../docs/SETUP_MAC.md#6-build-the-iphone-app-and-pair-it). The manual
steps are below.

## Design system

The app follows `design/MASTER.md`, the same design system as the web dashboard.

- **Tokens** come from `design/tokens.json`. `make ios-tokens` (also run by `make gen`) runs
  `scripts/gen-ios-tokens.mjs`, which writes `DesignSystem/Generated/DesignTokens.swift` (`NXColor` /
  `NXUIColor` with light and OLED dark values, `NXType` roles with Dynamic Type, `NXSpace`, `NXRadius`,
  `NXSize`, `NXMotion`, `NXShadow`) and `App/Assets.xcassets/AccentColor.colorset`. Never edit them by
  hand; `node scripts/gen-ios-tokens.mjs --check` fails when they are stale.
- **Fonts**: Montserrat (headings, numbers), Inter (body, UI) and JetBrains Mono (code, ids) as static
  TTF instances in `DesignSystem/Fonts`, from the Google Fonts builds published on npm
  (`@expo-google-fonts/inter`, `/montserrat`, `/jetbrains-mono`). They are licensed under the SIL Open
  Font License 1.1; the licence texts (`OFL-*.txt`) sit next to them and ship in the bundle. Static
  faces are used rather than the variable TTFs because `Font.custom(_:size:relativeTo:)` addresses a
  face by PostScript name, which a variable font registered with `UIAppFonts` does not expose per
  weight. Every face is listed in `UIAppFonts` in `App/Info.plist` and `Widgets/Info.plist`; the
  generator checks that.
- **Kit** (`DesignSystem/Kit`): buttons (primary, secondary, destructive, success, ghost, outline,
  confirm-twice), badges (task state, approval class), cards, rows, section tabs, stat tiles, meters
  and limit meters, empty / error / loading states, the approval card with the Face ID approve
  button, toasts, glass for bars and sheets. Everything honours Reduce Motion and Reduce
  Transparency, has 44 pt touch targets and VoiceOver labels, and draws the focus ring for keyboard
  focus. App-specific pieces (agent tiles, task rows, the store-backed approval card) are in
  `App/Components`.
- **Appearance** follows the system; Settings → Appearance (or More) overrides it with Light /
  System / Dark (stored under `nexus.settings.theme`, like the web).

## Generate and open the project

```sh
make ios-project          # from the repo root, or:
cd ios && xcodegen generate
open Nexus.xcodeproj
```

`Nexus.xcodeproj` is generated — don't commit it. Swift packages (connect-swift, swift-protobuf)
resolve on first build. The API code in `NexusKit/Sources/NexusAPI/Generated` comes from
`make gen` at the repo root; never edit it by hand.

## Configure bundle ids, App Group and team

Edit `Config/Nexus.xcconfig` (or create `Config/Local.xcconfig`, which is git-ignored and
included automatically):

```
NEXUS_BUNDLE_ID = com.yourname.nexus
NEXUS_APP_GROUP = group.com.yourname.nexus
DEVELOPMENT_TEAM = ABCDE12345
NEXUS_APS_ENVIRONMENT = development   // production for TestFlight / App Store
```

Extensions use `$(NEXUS_BUNDLE_ID).widgets`, `.notification-service`, `.notification-content`.
Register the App Group in the developer portal (Xcode's automatic signing does this when the
team is set). The App Group identifier is also used as the shared keychain access group and is
passed to code through each target's `Info.plist` key `NexusAppGroup`.

## Targets

| Target | Contents |
|---|---|
| `Nexus` | SwiftUI app: onboarding/QR pairing, Home, Inbox, Chat, Work (Tasks, Teams, Agents, Architect, Spend), More (Knowledge, Machines, Settings), approval sheet, app lock, push + Live Activity token registration, App Shortcuts |
| `NexusWidgets` | Run + Pulse Live Activities (Lock Screen + Dynamic Island), Home Screen widgets (inbox, machines, favourites), Lock Screen accessories, Control Center controls (kill switch, capture, talk) |
| `NexusNotificationService` | Decrypts HPKE envelopes (`nexus_envelope`), sets thread per task, downloads Hub-hosted attachments |
| `NexusNotificationContent` | Long-press UI (cart lines / diff summary / screenshot) |
| `Intents/` (folder) | App Intents compiled into both the app and the widget extension |
| `DesignSystem/` (folder) | Generated tokens, the component kit and the bundled fonts, compiled into both the app and the widget extension (see below) |
| `NexusKit` (local package) | `NexusAPI` (generated) + `NexusCore` (HubClient, keys, signing, pairing, event stream, store, shared models, mock data) |

`NexusKit` unit tests (byte layouts, JCS, envelopes): `cd ios/NexusKit && swift test` won't run on
Linux (CryptoKit/ActivityKit); run them from Xcode on a Mac (Product → Test with the package
scheme `NexusKit-Package`, iOS Simulator destination).

## Running against a local Hub

1. Start the Hub: `nexusd serve` (listens on `:7700`, HTTPS (TLS from the Hub's private CA, on by default)).
2. Make the phone reach it: same Wi-Fi (use the Mac's LAN IP), or away from home through the UniFi VPN (docs/NETWORK.md).
   ATS allows plain HTTP only for local networking (`NSAllowsLocalNetworking`).
3. Either
   - **Pair (recommended):** `nexusd pair -kind device` (or web → Security → Pair device) shows a
     `nexus://pair?...` QR. Scan it in the app, compare the 6-emoji safety code with the Hub,
     confirm. The app creates Secure Enclave keys and authenticates with a signed challenge.
   - **Dev token:** onboarding → "Connect with a dev token" → Hub URL + `security.dev_token`
     from `nexus.yaml`. Good for UI work; approvals still need paired device keys.

## Remote access (UniFi VPN — docs/NETWORK.md)

- All `hub=` URLs from the pairing QR are kept (`HubConfiguration.hubURLs`). `HubProbe`
  (`NexusCore/Networking/HubReachability.swift`) probes `GET /healthz` with a 3 s timeout, last
  working URL first (`HubRouteMemory`, App Group), before the app ever declares the Hub unreachable.
- `NWPathMonitor` (`NetworkMonitor`) re-probes on every path change (Wi-Fi ↔ cellular, VPN up/down;
  VPN tunnels are `.other` interfaces — the "via VPN" flag is best effort).
- `NexusStore.reachability`: `connected(url, viaVPN)`, `unreachable(reason)`, `offline`. Home and
  Chat show a calm "Can't reach your Nexus at home" banner with Retry; Settings → Remote access
  shows URL(s), route, last contact, the outbox and an offline WireGuard / Teleport how-to.
- While unreachable, chat commands, replies, captures and **Face ID-signed decisions** go to the
  `Outbox` (App Group, text only, idempotency keys) and are sent on reconnect; a signed decision
  that expires first is dropped with a message. Pending approvals are cached in the App Group so
  an approval notification can be reviewed and signed while away.
- Pair at home on Wi-Fi (pairing probes each QR URL and explains when none answers).

## Demo mode

Onboarding → "Explore demo mode" (or Settings → Demo mode) loads sample agents, nodes, tasks,
approvals and timelines from `MockData` — no Hub needed. SwiftUI previews use the same data.

## What needs a real device

- **Secure Enclave** keys (the Simulator falls back to software keys — clearly flagged in
  pairing and Settings; never use a Simulator pairing for real approvals).
- **Face ID** signing of approvals (Simulator: Features → Face ID → Enrolled works for prompts).
- **Push notifications** and the notification service extension (APNs tokens).
- **Live Activity push** (push-to-start and update tokens). Locally started activities
  ("Follow live" in task detail) work in the Simulator.
- Camera QR scanning (paste the `nexus://pair?...` link in the Simulator instead).

## Security notes / known gaps

- Byte formats follow `docs/WIRE_FORMATS.md` and live in `NexusCore/Security/WireFormat.swift`,
  `ApprovalSigner.swift`, `CanonicalJSON.swift`, `EnvelopeCrypto.swift`, `PairingService.swift`.
- The Hub verifies **every** approval decision with the Face ID-bound approval key, so
  Live Activity / notification Approve & Deny currently open the in-app approval sheet. If the
  Hub starts accepting the auth key for `approve`-level decisions, flip
  `ApprovalSignature.backgroundDecisionsUseAuthKey`.
- Not yet implemented: mTLS client certificate + CA pinning (fingerprint is stored), Layer-3
  signed request headers on every call (the signer exists: `WireFormat.signedRequestHeaders`),
  App Attest at pairing, owner root key / signed kill-switch statement, sealed approval previews,
  Watch app, share extension.
