# Security Model

> Status: draft v0.1

Nexus can run code on your machines, read your email and spend your money. Security is a core
feature, not an add-on.

## 1. Identities

| Identity | Credential |
|---|---|
| **Hub** | TLS certificate (public CA via Caddy/ACME, or self-signed pinned at pairing). |
| **Node** | Client certificate issued by the Hub's internal CA at pairing (mTLS). |
| **Device** (iPhone, browser, CLI) | Key pair; private key in Secure Enclave (iOS) / WebCrypto non-extractable (web) / file (CLI). Short-lived access tokens obtained by signing a challenge. |
| **Agent run** | Scoped run token: only the tools, notes and nodes its agent allows; expires with the run. |

## 2. Pairing

1. `nexusd pair --device` (or the web Security page) shows a QR: hub URL, one-time token (5 min),
   hub certificate fingerprint.
2. The phone scans, pins the fingerprint, generates its key pair, sends the public key + token.
3. The Hub registers the device with a name and permissions; the token is burned.

Nodes pair the same way (`nexus-node pair <code>`), receiving a client certificate.

## 3. Trust levels and approvals

See [AGENTS.md §7](AGENTS.md#7-trust-and-autonomy) for action classes. Enforcement happens **on the
Hub** (policy engine) and is re-checked **on the Node** before executing a tool, so a compromised
client cannot bypass it.

- **Biometric approvals:** the Hub sends a challenge containing a hash of the exact action payload
  (e.g. cart total + items). The phone shows the payload, requires Face ID, signs the challenge with
  the Secure Enclave key. The Hub verifies the signature before releasing the action. Approving one
  thing can never approve something else.
- Approvals expire; expired = denied.
- Notification actions for `approve` level work from the lock screen; `biometric` always requires
  Face ID.

## 4. Secrets

- Provider keys, cookies, browser profiles and SSH keys live **on the Node that uses them**.
- The Hub stores secret *references*; runs receive secrets as environment/handles on the Node only.
- Secrets are redacted from event streams and logs (pattern + known-value redaction).

## 5. Isolation

- Each run has its own workspace directory; coding runs use git worktrees.
- Optional container sandbox (rootless) per run with CPU/memory/network limits.
- Shell runtime only runs whitelisted templates unless the agent policy allows free commands
  (which then are `approve` by default).

## 6. Prompt-injection posture

Agents read untrusted content (web pages, emails, shared files). Mitigations:

- Untrusted content is tagged as such in context; tools that act externally always go through
  policy regardless of what the model "decides".
- Spend/destructive/external actions require approval by default — injection can at most propose.
- Per-agent tool allow-lists keep blast radius small (the grocery agent cannot send email).
- Suspicious instructions found in content are surfaced in the run view.

## 7. Audit and recovery

- Every action, approval and decision is an event with actor (device/agent/node) and timestamp.
- Vault writes are git commits attributed to the run → revert any agent's change.
- **Kill switch** on iOS (widget, Control Center, app) and web: cancels all runs, pauses triggers,
  revokes run tokens.
- Lost phone: revoke the device from web or CLI; its keys are useless afterwards.

## 8. Threat model (summary)

| Threat | Mitigation |
|---|---|
| Stolen phone | Device keys in Secure Enclave, Face ID for sensitive actions, remote revoke. |
| Hub exposed to internet | TLS, no unauthenticated endpoints except pairing (rate-limited, one-time tokens), optional Tailscale-only mode. |
| Compromised Node | Node can only act on runs assigned to it; mTLS cert revocable; secrets of other nodes never sent to it. |
| Malicious content → agent | Tool allow-lists, policy enforcement outside the model, approvals. |
| Runaway costs | Budgets with hard stops, cost events, alerts. |
| Bad agent edit (by an agent) | Agent/policy notes changes require approval; git history. |
