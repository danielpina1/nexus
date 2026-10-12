# ADR-0009: The UniFi Gateway is the only way into Nexus

- Status: accepted
- Date: 2026-10-12

## Context
The user runs a Ubiquiti UniFi Gateway at home and wants the iPhone app to reach the Nexus machine
through it. Nexus must not be exposed to the internet.

## Decision
The Hub runs on a home machine on a dedicated network behind the UniFi Gateway. Clients reach it:
directly on the home network, and from outside only through the gateway's VPN (WireGuard VPN
server with iOS on-demand, or Teleport). No port forwarding to Nexus. The Hub makes only outbound
connections (APNs, optional cloud model APIs). Setup is documented in NETWORK.md.

## Consequences
- The phone app has one Hub URL (`https://nexus.home.arpa:7700`) that works at home and away.
- Push notifications work without the VPN (they come from Apple); actions need the tunnel, which
  on-demand rules bring up automatically.
- All SECURITY.md layers still apply inside the tunnel.
