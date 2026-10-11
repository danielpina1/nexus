# ADR-0002: Protobuf + Connect-RPC as the API

- Status: accepted
- Date: 2026-10-11

## Context
Three clients (SwiftUI, React, Go CLI) plus Go nodes must share one API with streaming, and stay in
sync as the API evolves.

## Decision
Define the API in Protobuf (`proto/nexus/v1`), manage it with `buf` (lint + breaking-change checks),
and serve it with Connect (`connect-go`). Generate clients with `connect-es` (web) and
`connect-swift` (iOS).

## Consequences
- One schema → typed clients everywhere; breaking changes caught in CI.
- Connect speaks gRPC, gRPC-Web and plain JSON/HTTP from the same handler: browsers work without a
  proxy and `curl` works for debugging.
- Server streaming for events; bidirectional streaming for Node↔Hub (HTTP/2 only, fine for Go↔Go).
- Clients use server streaming only (browser-compatible); client→server actions are unary calls.
