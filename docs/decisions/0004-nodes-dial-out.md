# ADR-0004: Nodes dial out to the Hub

- Status: accepted
- Date: 2026-10-11

## Context
Machines sit behind NAT, home routers and firewalls. The phone must reach them from anywhere.

## Decision
Each Node keeps one outbound, mTLS-authenticated, bidirectional stream to the Hub
(`NodeLinkService.Connect`). All commands to a Node travel over that stream. Only the Hub is exposed.

## Consequences
- No port forwarding or VPN required (Tailscale remains an option).
- The Hub is a single point of coordination; Nodes keep running in-flight work and buffer events
  during short Hub outages, resuming with a cursor.
- Direct Node↔phone data transfer (large files) goes through the Hub or pre-signed blob URLs.
