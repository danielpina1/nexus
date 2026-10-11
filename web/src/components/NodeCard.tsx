import { Link } from "react-router";
import { Battery, Cpu, HardDrive, MemoryStick, Play } from "lucide-react";
import type { Node, NodeStatus } from "../gen/nexus/v1/node_pb";
import { cx, timeAgo, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { Badge } from "./ui/Badge";
import { Meter } from "./ui/Meter";

export function OnlineDot({ online, className }: { online: boolean; className?: string }) {
  return <span className={cx("inline-block size-2 shrink-0 rounded-full", online ? "bg-ok" : "bg-muted", className)} />;
}

function Metric({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <div className="min-w-0">
      <div className="mb-1 flex items-center justify-between text-[11px] text-muted">
        <span className="flex items-center gap-1">
          {icon}
          {label}
        </span>
        <span className={cx("tabular font-medium", value >= 90 ? "text-danger" : value >= 75 ? "text-warn" : "text-fg-2")}>{Math.round(value)}%</span>
      </div>
      <Meter value={value} label={label} />
    </div>
  );
}

export function NodeMetrics({ status }: { status?: NodeStatus }) {
  return (
    <div className="grid grid-cols-3 gap-3">
      <Metric icon={<Cpu className="size-3" />} label="CPU" value={status?.cpuPercent ?? 0} />
      <Metric icon={<MemoryStick className="size-3" />} label="Mem" value={status?.memoryPercent ?? 0} />
      <Metric icon={<HardDrive className="size-3" />} label="Disk" value={status?.diskPercent ?? 0} />
    </div>
  );
}

export function NodeCard({ node, status }: { node: Node; status?: NodeStatus }) {
  const now = useNow(10_000);
  const st = status ?? node.status;
  const online = !!st?.online;
  return (
    <Link to={`/machines/${node.id}`} className="block rounded-xl border border-border bg-surface p-4 shadow-card transition-colors hover:border-border-strong">
      <div className="mb-3 flex items-start gap-3">
        <div className="flex size-9 items-center justify-center rounded-lg bg-surface-2 text-fg-2">
          <Cpu className="size-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-[14px] font-semibold text-fg">{node.name}</span>
            <OnlineDot online={online} />
          </div>
          <div className="truncate text-xs text-muted">
            {node.os} · {node.arch} · {online ? `seen ${timeAgo(tsMs(st?.lastSeen), now)}` : "offline"}
          </div>
        </div>
        {(st?.activeRuns ?? 0) > 0 && (
          <Badge tone="info" dot pulse>
            <Play className="hidden" />
            {st?.activeRuns} running
          </Badge>
        )}
      </div>
      <NodeMetrics status={st} />
      {(st?.batteryPercent ?? 0) > 0 && (
        <div className="mt-2 flex items-center gap-1 text-xs text-muted">
          <Battery className="size-3" /> {Math.round(st!.batteryPercent)}%
        </div>
      )}
      <div className="mt-3 flex flex-wrap gap-1">
        {node.runtimes.map((r) => (
          <Badge key={r.name} tone={r.available ? "neutral" : "warn"} title={`${r.name} ${r.version}${r.available ? "" : " (unavailable)"}`}>
            {r.name}
          </Badge>
        ))}
      </div>
    </Link>
  );
}
