import { create } from "@bufbuild/protobuf";
import { NodeSchema, type Node } from "../gen/nexus/v1/node_pb";
import { DeviceKind, DeviceSchema, type Device } from "../gen/nexus/v1/device_pb";
import { DAY, HOUR, MIN, randomBytes, ts } from "./util";

export function makeNodes(now: number): Node[] {
  return [
    create(NodeSchema, {
      id: "mac-studio",
      name: "mac-studio",
      os: "macOS 26.1",
      arch: "arm64",
      tags: ["dev", "browser", "always-on"],
      runtimes: [
        { name: "claude-code", version: "2.4.1", available: true },
        { name: "codex", version: "0.61.0", available: true },
        { name: "browser", version: "Chromium 141", available: true },
        { name: "ollama", version: "0.12.3", available: true },
        { name: "shell", version: "zsh 5.9", available: true },
      ],
      status: { nodeId: "mac-studio", online: true, cpuPercent: 38, memoryPercent: 61, diskPercent: 54, activeRuns: 2, lastSeen: ts(now) },
      pairedAt: ts(now - 41 * DAY),
    }),
    create(NodeSchema, {
      id: "vps",
      name: "vps",
      os: "Debian 13",
      arch: "amd64",
      tags: ["always-on", "server", "any"],
      runtimes: [
        { name: "shell", version: "bash 5.2", available: true },
        { name: "openai", version: "gateway", available: true },
        { name: "docker", version: "28.4", available: true },
      ],
      status: { nodeId: "vps", online: true, cpuPercent: 7, memoryPercent: 43, diskPercent: 62, activeRuns: 0, lastSeen: ts(now) },
      pairedAt: ts(now - 52 * DAY),
    }),
    create(NodeSchema, {
      id: "gpu-box",
      name: "gpu-box",
      os: "Ubuntu 24.04",
      arch: "amd64",
      tags: ["gpu", "any"],
      runtimes: [
        { name: "ollama", version: "0.12.3", available: true },
        { name: "vllm", version: "0.11.0", available: false },
        { name: "shell", version: "bash 5.2", available: true },
      ],
      status: { nodeId: "gpu-box", online: true, cpuPercent: 72, memoryPercent: 78, diskPercent: 91, activeRuns: 1, lastSeen: ts(now) },
      pairedAt: ts(now - 30 * DAY),
    }),
  ];
}

export function makeDevices(now: number): Device[] {
  return [
    create(DeviceSchema, {
      id: "dev_iphone",
      name: "Daniel's iPhone 17 Pro",
      kind: DeviceKind.IOS,
      publicKey: randomBytes(65),
      approvalPublicKey: randomBytes(65),
      encryptionPublicKey: randomBytes(32),
      pairedAt: ts(now - 41 * DAY),
      lastSeen: ts(now - 2 * MIN),
      primary: true,
    }),
    create(DeviceSchema, {
      id: "dev_watch",
      name: "Apple Watch Ultra",
      kind: DeviceKind.WATCH,
      publicKey: randomBytes(65),
      pairedAt: ts(now - 40 * DAY),
      lastSeen: ts(now - 25 * MIN),
    }),
    create(DeviceSchema, {
      id: "web",
      name: "Web dashboard (this browser)",
      kind: DeviceKind.WEB,
      publicKey: randomBytes(65),
      pairedAt: ts(now - 12 * DAY),
      lastSeen: ts(now),
    }),
    create(DeviceSchema, {
      id: "dev_cli",
      name: "nexus CLI on mac-studio",
      kind: DeviceKind.CLI,
      publicKey: randomBytes(65),
      pairedAt: ts(now - 39 * DAY),
      lastSeen: ts(now - 3 * HOUR),
    }),
  ];
}

interface FileSpec {
  name: string;
  dir?: boolean;
  size?: number;
  ageMin?: number;
}

const tree: Record<string, Record<string, FileSpec[]>> = {
  "mac-studio": {
    "/Users/daniel": [
      { name: "code", dir: true },
      { name: "Documents", dir: true },
      { name: "Downloads", dir: true },
      { name: ".nexus", dir: true },
      { name: ".zshrc", size: 2314, ageMin: 9000 },
    ],
    "/Users/daniel/code": [
      { name: "nexus", dir: true, ageMin: 6 },
      { name: "anthrex", dir: true, ageMin: 2000 },
      { name: "dotfiles", dir: true, ageMin: 40000 },
    ],
    "/Users/daniel/code/nexus": [
      { name: "backend", dir: true, ageMin: 6 },
      { name: "docs", dir: true, ageMin: 300 },
      { name: "ios", dir: true, ageMin: 900 },
      { name: "proto", dir: true, ageMin: 1300 },
      { name: "web", dir: true, ageMin: 20 },
      { name: "Makefile", size: 1840, ageMin: 1300 },
      { name: "README.md", size: 5210, ageMin: 1300 },
      { name: "buf.gen.yaml", size: 612, ageMin: 1300 },
    ],
    "/Users/daniel/.nexus": [
      { name: "node.yaml", size: 1204, ageMin: 4000 },
      { name: "keys", dir: true, ageMin: 50000 },
      { name: "worktrees", dir: true, ageMin: 6 },
      { name: "logs", dir: true, ageMin: 1 },
    ],
    "/Users/daniel/Downloads": [
      { name: "continente-cart-2026-10-04.png", size: 412_331, ageMin: 10000 },
      { name: "invoice-hetzner-sep.pdf", size: 88_120, ageMin: 15000 },
    ],
  },
  vps: {
    "/srv": [
      { name: "nexus", dir: true, ageMin: 30 },
      { name: "backups", dir: true, ageMin: 400 },
      { name: "caddy", dir: true, ageMin: 30000 },
    ],
    "/srv/nexus": [
      { name: "nexusd", size: 48_211_002, ageMin: 1500 },
      { name: "nexus.yaml", size: 2911, ageMin: 1500 },
      { name: "data", dir: true, ageMin: 1 },
      { name: "vault", dir: true, ageMin: 3 },
    ],
    "/srv/backups": [
      { name: "nexus-2026-10-11.tar.zst", size: 312_000_000, ageMin: 400 },
      { name: "nexus-2026-10-10.tar.zst", size: 309_400_000, ageMin: 1840 },
      { name: "nexus-2026-10-09.tar.zst", size: 305_100_000, ageMin: 3280 },
    ],
  },
  "gpu-box": {
    "/home/daniel": [
      { name: "models", dir: true, ageMin: 900 },
      { name: "docker", dir: true, ageMin: 2000 },
      { name: ".nexus", dir: true, ageMin: 2 },
    ],
    "/home/daniel/models": [
      { name: "qwen3-14b-q5.gguf", size: 10_400_000_000, ageMin: 9000 },
      { name: "llama-4-scout-q4.gguf", size: 21_800_000_000, ageMin: 20000 },
      { name: "nomic-embed-v2.gguf", size: 540_000_000, ageMin: 30000 },
      { name: "router-classifier-v3.safetensors", size: 1_200_000_000, ageMin: 900 },
    ],
  },
};

export const nodeHome: Record<string, string> = { "mac-studio": "/Users/daniel", vps: "/srv", "gpu-box": "/home/daniel" };

export function listFiles(nodeId: string, path: string, now: number) {
  const home = nodeHome[nodeId] ?? "/";
  const p = (path || home).replace(/\/+$/, "") || "/";
  const specs: FileSpec[] = tree[nodeId]?.[p] ?? [
    { name: "README.md", size: 1024, ageMin: 600 },
    { name: "notes.txt", size: 312, ageMin: 1200 },
  ];
  return specs
    .map((f) => ({
      name: f.name,
      path: `${p === "/" ? "" : p}/${f.name}`,
      isDir: !!f.dir,
      sizeBytes: BigInt(f.size ?? 0),
      modifiedAt: ts(now - (f.ageMin ?? 60) * MIN),
    }))
    .sort((a, b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name));
}
