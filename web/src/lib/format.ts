import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { Cost, Money } from "../gen/nexus/v1/common_pb";
import { ActionClass, Tier, TrustLevel } from "../gen/nexus/v1/common_pb";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { DeviceKind } from "../gen/nexus/v1/device_pb";
import { NoticeKind } from "../gen/nexus/v1/event_pb";

export function tsDate(ts: Timestamp | undefined): Date | undefined {
  return ts ? timestampDate(ts) : undefined;
}

export function tsMs(ts: Timestamp | undefined): number {
  return ts ? timestampDate(ts).getTime() : 0;
}

export function moneyMinor(m: Money | undefined): number {
  return m ? Number(m.minorUnits) : 0;
}

export function costMinor(c: Cost | undefined): number {
  return moneyMinor(c?.amount);
}

export function formatMoney(minor: number, currency = "EUR", opts?: { compact?: boolean }): string {
  const value = minor / 100;
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "EUR",
      minimumFractionDigits: opts?.compact && value >= 100 ? 0 : 2,
      maximumFractionDigits: opts?.compact && value >= 100 ? 0 : 2,
    }).format(value);
  } catch {
    return `${value.toFixed(2)} ${currency}`;
  }
}

export function formatTokens(n: number | bigint): string {
  const v = Number(n);
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`;
  return String(v);
}

export function formatBytes(n: number | bigint): string {
  let v = Number(n);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "short" });

export function timeAgo(d: Date | number | undefined, now = Date.now()): string {
  if (d == null) return "—";
  const ms = typeof d === "number" ? d : d.getTime();
  if (!ms) return "—";
  const diff = (ms - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 10) return "just now";
  if (abs < 60) return rtf.format(Math.round(diff), "second");
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
  return rtf.format(Math.round(diff / 86400), "day");
}

export function formatDuration(ms: number): string {
  if (ms < 0) ms = 0;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

export function formatClock(d: Date | undefined): string {
  if (!d) return "";
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatDateTime(d: Date | undefined): string {
  if (!d) return "—";
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// ---- enum labels ----------------------------------------------------------------------------

export const taskStateLabel: Record<TaskState, string> = {
  [TaskState.UNSPECIFIED]: "Unknown",
  [TaskState.QUEUED]: "Queued",
  [TaskState.ROUTING]: "Routing",
  [TaskState.NEEDS_INPUT]: "Needs input",
  [TaskState.ASSIGNED]: "Assigned",
  [TaskState.RUNNING]: "Running",
  [TaskState.WAITING_APPROVAL]: "Waiting approval",
  [TaskState.WAITING_INPUT]: "Waiting input",
  [TaskState.ESCALATING]: "Escalating",
  [TaskState.SUCCEEDED]: "Succeeded",
  [TaskState.FAILED]: "Failed",
  [TaskState.CANCELLED]: "Cancelled",
};

export type Tone = "neutral" | "accent" | "ok" | "warn" | "danger" | "info";

export function taskStateTone(s: TaskState): Tone {
  switch (s) {
    case TaskState.RUNNING:
    case TaskState.ASSIGNED:
    case TaskState.ROUTING:
    case TaskState.ESCALATING:
      return "info";
    case TaskState.WAITING_APPROVAL:
    case TaskState.WAITING_INPUT:
    case TaskState.NEEDS_INPUT:
      return "warn";
    case TaskState.SUCCEEDED:
      return "ok";
    case TaskState.FAILED:
      return "danger";
    default:
      return "neutral";
  }
}

export const ACTIVE_STATES = new Set<TaskState>([
  TaskState.QUEUED,
  TaskState.ROUTING,
  TaskState.NEEDS_INPUT,
  TaskState.ASSIGNED,
  TaskState.RUNNING,
  TaskState.WAITING_APPROVAL,
  TaskState.WAITING_INPUT,
  TaskState.ESCALATING,
]);

export const TERMINAL_STATES = new Set<TaskState>([TaskState.SUCCEEDED, TaskState.FAILED, TaskState.CANCELLED]);

export function isActive(s: TaskState): boolean {
  return ACTIVE_STATES.has(s);
}

export const tierLabel: Record<Tier, string> = {
  [Tier.UNSPECIFIED]: "auto",
  [Tier.SIMPLE]: "simple",
  [Tier.MEDIUM]: "medium",
  [Tier.HEAVY]: "heavy",
};

export const trustLabel: Record<TrustLevel, string> = {
  [TrustLevel.UNSPECIFIED]: "default",
  [TrustLevel.AUTO]: "auto",
  [TrustLevel.NOTIFY]: "notify",
  [TrustLevel.APPROVE]: "approve",
  [TrustLevel.BIOMETRIC]: "biometric",
};

export const actionClassLabel: Record<ActionClass, string> = {
  [ActionClass.UNSPECIFIED]: "action",
  [ActionClass.READ]: "read",
  [ActionClass.WRITE_INTERNAL]: "write (internal)",
  [ActionClass.COMPUTE]: "compute",
  [ActionClass.WRITE_EXTERNAL]: "write (external)",
  [ActionClass.CODE_PUSH]: "code push",
  [ActionClass.SPEND]: "spend",
  [ActionClass.DESTRUCTIVE]: "destructive",
  [ActionClass.DEVICE_CONTROL]: "device control",
};

export const deviceKindLabel: Record<DeviceKind, string> = {
  [DeviceKind.UNSPECIFIED]: "Device",
  [DeviceKind.IOS]: "iPhone",
  [DeviceKind.WATCH]: "Apple Watch",
  [DeviceKind.WEB]: "Web",
  [DeviceKind.CLI]: "CLI",
};

export const noticeKindLabel: Record<NoticeKind, string> = {
  [NoticeKind.UNSPECIFIED]: "Notice",
  [NoticeKind.RESULT]: "Result",
  [NoticeKind.ALERT]: "Alert",
  [NoticeKind.SUGGESTION]: "Suggestion",
  [NoticeKind.FACT_REVIEW]: "Fact to confirm",
  [NoticeKind.BRIEF]: "Brief",
};

/** Local runtimes cost nothing per token; everything else is "cloud". */
const LOCAL_RUNTIMES = new Set(["ollama", "llama.cpp", "vllm", "mlx", "local", "shell", "echo"]);
export function isLocalRuntime(runtime: string, model = ""): boolean {
  return LOCAL_RUNTIMES.has(runtime) || model.startsWith("fast-local") || model.startsWith("ollama/");
}

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

export function shortId(id: string): string {
  const i = id.indexOf("_");
  const tail = i >= 0 ? id.slice(i + 1) : id;
  return tail.length > 8 ? tail.slice(-8).toLowerCase() : tail;
}
