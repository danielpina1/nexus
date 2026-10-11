# ADR-0008: Zero-trust Hub — Nodes verify authority themselves

- Status: accepted
- Date: 2026-10-11

## Context
The phone ↔ machine connection must be "ultra safe". The Hub is the most exposed component
(it may run on a VPS, it talks to APNs, it parses untrusted input). If trust is concentrated in the
Hub, compromising it means controlling every machine.

## Decision
- Authority comes from an **owner root key** in the primary iPhone's Secure Enclave (with an offline
  recovery key), not from the Hub. It signs device/node enrolments, revocations, policy and updates.
- Nodes store the owner root public key and **verify** device-signed approvals (bound to the exact
  action hash) before any `code_push`, `spend`, `destructive`, `write_external` or direct shell action.
- Sensitive phone ↔ node payloads use **sealed envelopes** (HPKE + signatures); the Hub only routes.
- Transport is a private network (WireGuard/Tailscale) + mTLS 1.3 with a pinned private CA.
- Notification contents are end-to-end encrypted and decrypted on-device.

## Consequences
- A compromised Hub can disrupt and observe metadata but cannot perform dangerous actions on Nodes.
- More moving parts: key management, pairing ceremony with safety codes, revocation propagation,
  recovery flow. Requires careful implementation with standard primitives only (CryptoKit, Go
  stdlib) and an external review before handling real money.
- Autonomous (Hub-initiated) runs remain possible but are limited to auto-level tools within
  owner-signed policy.
