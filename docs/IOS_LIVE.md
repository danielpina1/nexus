# iOS Live Surfaces — Live Activities, Notifications, Widgets

> Status: draft v0.1. Goal: see and control agents **without opening the app**.

## 1. Surfaces

| Surface | Nexus use |
|---|---|
| **Live Activities** (Lock Screen) | Running task: step, progress, cost, approve button |
| **Dynamic Island** | Same, compact, visible while using other apps |
| **StandBy** | Full-screen "mission control" while charging on the desk / nightstand |
| **Apple Watch Smart Stack** | Live Activities mirrored automatically |
| **CarPlay / Mac menu bar** | iPhone Live Activities shown there on recent OS versions |
| **Rich notifications** | Approvals with screenshots/diffs, inline reply, choices |
| **Interactive widgets** | Run workflows, status, kill switch |
| **Control Center / Lock Screen controls / Action Button** | Capture, talk to Nexus, workflow, kill switch |

## 2. Live Activities

### 2.1 Activity types

| Type | When | Content |
|---|---|---|
| **Pulse** (aggregate, default) | Any time agents are active | "3 running · 1 waiting for you · €0.42 today" — one activity instead of many (iOS limits concurrent activities per app). |
| **Run** | Tasks you follow ("Follow live") or agents marked `live: true` | Agent icon, step, progress, cost, elapsed, node. |
| **Incident** | Watchdog alerts | Machine, symptom, auto-fix status; stays until resolved. |
| **Workflow** | Multi-step workflows | "Step 3/6 — Buying ingredients". |

### 2.2 States

`running` → `waitingForYou` (approval or question; alert + highlight) → `done` (summary, lingers)
| `failed` (error + Retry).

```
Dynamic Island — compact
 ┌─────────────────────────────────┐
 │ 🛒 14/22          ●●●○  €61.20  │   filling the cart
 └─────────────────────────────────┘
 ┌─────────────────────────────────┐
 │ 🔨 tests ✓        ⏳ approve?   │   waiting for approval
 └─────────────────────────────────┘

Dynamic Island — expanded
 ┌───────────────────────────────────────────┐
 │ 🛒 Grocery · Continente        mac-mini   │
 │ Cart ready — 22 items · €84.30            │
 │ 2 substitutions · delivery Sat 9–11       │
 │ [ Review ]  [ Approve with Face ID ]      │
 └───────────────────────────────────────────┘

Lock Screen
 ┌───────────────────────────────────────────┐
 │ 🔨 Code fixer — nexus                     │
 │ ▓▓▓▓▓▓▓▓▓▓▓▓░░░  Running tests (4/5)       │
 │ +42 −13 · 3 files · €0.81 · 6 min         │
 │ [ Open ]   [ Try harder ]   [ Stop ]      │
 └───────────────────────────────────────────┘
```

### 2.3 Attributes (Swift sketch)

```swift
struct RunActivityAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        enum Phase: String, Codable { case running, waitingApproval, waitingInput, done, failed }
        var phase: Phase
        var title: String          // "Running tests"
        var detail: String?        // "+42 −13 · 3 files" (redacted per privacy setting)
        var progress: Double?      // 0...1, nil = indeterminate
        var step: Int?
        var totalSteps: Int?
        var costCents: Int
        var approvalId: String?
        var approvalLevel: String? // "approve" | "biometric"
        var choices: [String]?     // for simple questions
    }
    var taskId: String
    var agentId: String
    var agentName: String
    var agentSymbol: String        // SF Symbol
    var nodeName: String
}
```

### 2.4 Buttons (App Intents)

| Button | Mechanism |
|---|---|
| Approve / Deny (any level) | Deep link to the in-app approval sheet → Face ID → signature with the biometry-bound approval key → `ApprovalService.Decide` |

> **Decision (ultra-safe default):** every approval decision — approve or deny, any trust level — is
> signed with the Face ID-bound approval key and verified by the hub (and by nodes). Background
> approval from a Live Activity or notification is therefore not possible; the buttons open the
> approval sheet. The app keeps a switch (`ApprovalSignature.backgroundDecisionsUseAuthKey`) in case a
> future policy allows auth-key signatures for `approve`-level actions.
| Choice (simple question) | Background intent → `TaskService.SendMessage` |
| Stop / Try harder / Pause | Background intent → `CancelTask` / `EscalateTask` |

### 2.5 Remote lifecycle (Hub-driven)

- **Push-to-start:** the app registers a push-to-start token per activity type
  (`DeviceService.RegisterLiveActivityToken`, kind `PUSH_TO_START`). The Hub can start an activity
  when a task begins without the phone (cron, web, webhook, watchdog).
- **Update:** each activity reports its own push token (`kind = UPDATE`, with `activity_id`); the
  Hub sends `liveactivity` pushes on state changes.
- **End:** final state + dismissal date (e.g. keep the result for 1 h).
- **Follow from anywhere:** `TaskService.FollowTask` (from web or phone) asks the phone to show a
  Run activity for that task (via push-to-start).

Example update payload:

```json
{
  "aps": {
    "timestamp": 1760220000,
    "event": "update",
    "content-state": {
      "phase": "waitingApproval", "title": "Cart ready", "detail": "22 items · €84.30",
      "progress": 1.0, "costCents": 31, "approvalId": "apr_01J...", "approvalLevel": "biometric"
    },
    "stale-date": 1760223600,
    "relevance-score": 100,
    "alert": { "title": "Approval needed", "body": "Checkout at Continente" }
  }
}
```

### 2.6 Hub `liveactivity` manager rules

- **Coalesce** run events; progress updates at most every 5–15 s with low priority (5).
- **State changes** (waiting for you, done, failed) immediately with high priority (10) + alert.
- **Payload < 4 KB:** ids, short strings, numbers; details are fetched when the app opens.
- **Duration:** activities live ~8 h; longer tasks are folded into Pulse or restarted.
- **stale-date** on every update so a lost connection shows as "last update N min ago".
- **relevance-score** so the Dynamic Island shows the task that needs you.
- Track tokens per device and activity; drop on `410 Unregistered`.
- Respect privacy setting: `detail` can be "hidden on Lock Screen" per agent/category.

## 3. Notifications

### 3.1 Categories

| Category | Interruption level | Actions |
|---|---|---|
| `APPROVAL` | time-sensitive | Approve · Deny · Details |
| `APPROVAL_BIOMETRIC` | time-sensitive | Review & Face ID (foreground) · Deny |
| `QUESTION` | active | choice buttons · Reply (text input) |
| `RESULT` | active / passive | Open · Save to vault · Share |
| `ALERT` | time-sensitive | Investigate · Auto-fix · Mute 1 h |
| `BRIEF` | passive (summary) | Open |
| `FACT_REVIEW` | passive | Correct · Wrong |
| `SUGGESTION` | passive | Do it · Not now · Never |

All approval actions use `.authenticationRequired` (device must be unlocked).

### 3.2 Behaviours

- **Agents as contacts:** communication notifications (`INSendMessageIntent`) show each agent's
  avatar and name; Focus can allow specific agents through.
- **End-to-end encrypted content:** pushes carry an HPKE envelope; the **Notification Service
  Extension** decrypts on-device and downloads attachments (cart screenshot, diff image).
- **Notification Content Extension:** custom long-press UI — cart list with prices, code diff,
  choices with photos.
- **Thread per task:** `thread-id = task_id`.
- **Replace, don't duplicate:** `apns-collapse-id = approval_id`; approving on the web replaces the
  phone notification with "✓ Approved on web".
- **Digest:** passive items are batched; only things that need you interrupt you.
- **Focus-aware:** Focus filter decides which agents/categories may notify.
- **Escalating reminders:** unanswered approval → time-sensitive reminder after 15 min → expires
  as "denied (expired)".

## 4. Widgets and controls

- **Home Screen:** inbox count, machines grid, favourite workflows (interactive buttons), last result.
- **Lock Screen:** pending approvals, running tasks.
- **Control Center controls:** Quick capture, Talk to Nexus, Run workflow…, **Kill switch**.
- **Action Button:** talk to Nexus or a chosen workflow.
- **StandBy:** Pulse full screen; overnight agents at a glance.

## 5. Apple Watch

Approvals (normal level), Live Activities in the Smart Stack, raise-to-speak commands and answers,
complication with pending approvals. Biometric approvals require the iPhone (configurable).

## 6. iOS project structure

| Target | Contents |
|---|---|
| `Nexus` (app) | SwiftUI app, connect-swift clients, SwiftData cache, approval sheet |
| `NexusWidgets` | Widgets, Live Activities, Control Center controls |
| `NexusNotificationService` | Decrypt envelopes, fetch attachments, communication intents |
| `NexusNotificationContent` | Custom notification UI (cart, diff, choices) |
| `NexusIntents` (shared) | App Intents for Siri, Shortcuts, Live Activity buttons, widgets |
| `NexusWatch` | watchOS app + complications |
| `NexusKit` (package) | Shared models, API client, crypto (keys, envelopes), App Group storage |

Capabilities: Push Notifications, Time Sensitive Notifications, Communication Notifications,
App Groups, Keychain Sharing (app group only), App Attest. Critical alerts are not used (special
Apple entitlement).

## 7. Delivery order

1. Notifications with Approve / Deny / Reply, thread per task, collapse on resolution.
2. Run Live Activity started in-app + Hub updates.
3. Push-to-start + Pulse activity + Follow from web.
4. Encrypted notifications, rich attachments, custom content UI.
5. Widgets, controls, Action Button, StandBy.
6. Watch app, agents-as-contacts.
