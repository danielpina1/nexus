import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { Search, SearchX } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Card } from "../components/ui/Card";
import { Input } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { EmptyState } from "../components/ui/EmptyState";
import { SkeletonRows } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { TaskRow } from "../components/TaskRow";
import { VirtualList } from "../components/VirtualList";
import { useAllTasks } from "../lib/selectors";
import { useAgents, useNodes, useTaskList } from "../lib/queries";
import { costMinor, formatMoney, isActive, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";

type Status = "all" | "active" | "waiting" | "succeeded" | "failed" | "cancelled";
const WAITING = new Set([TaskState.WAITING_APPROVAL, TaskState.WAITING_INPUT, TaskState.NEEDS_INPUT]);

function matches(t: Task, s: Status) {
  switch (s) {
    case "active":
      return isActive(t.state);
    case "waiting":
      return WAITING.has(t.state);
    case "succeeded":
      return t.state === TaskState.SUCCEEDED;
    case "failed":
      return t.state === TaskState.FAILED;
    case "cancelled":
      return t.state === TaskState.CANCELLED;
    default:
      return true;
  }
}

const RANGES: Record<string, number> = { "24h": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000, all: Infinity };

function Select({ value, onChange, children, label }: { value: string; onChange: (v: string) => void; children: React.ReactNode; label: string }) {
  return (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 rounded-lg border border-border bg-surface px-2.5 text-[13px] text-fg outline-none focus:border-accent/60 pointer-coarse:h-11"
    >
      {children}
    </select>
  );
}

export default function Tasks() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") as Status) || "all";
  const agent = params.get("agent") ?? "";
  const node = params.get("node") ?? "";
  const range = params.get("range") ?? "7d";
  const q = params.get("q") ?? "";
  const roots = params.get("roots") !== "0";
  const set = (k: string, v: string) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        if (v) n.set(k, v);
        else n.delete(k);
        return n;
      },
      { replace: true },
    );

  const list = useTaskList({});
  const all = useAllTasks();
  const { data: agents } = useAgents();
  const { data: nodes } = useNodes();

  const children = useMemo(() => {
    const m = new Map<string, number>();
    for (const t of all) if (t.parentId) m.set(t.parentId, (m.get(t.parentId) ?? 0) + 1);
    return m;
  }, [all]);

  const now = useNow(60_000);
  const filtered = useMemo(() => {
    const since = now - RANGES[range];
    const text = q.toLowerCase();
    return all.filter(
      (t) =>
        (!roots || !t.parentId || status === "active" || status === "waiting") &&
        matches(t, status) &&
        (!agent || t.agentId === agent) &&
        (!node || t.nodeId === node) &&
        tsMs(t.createdAt) >= since &&
        (!text || `${t.title} ${t.prompt} ${t.summary}`.toLowerCase().includes(text)),
    );
  }, [all, status, agent, node, range, q, roots, now]);

  const counts = useMemo(() => {
    const c: Record<Status, number> = { all: 0, active: 0, waiting: 0, succeeded: 0, failed: 0, cancelled: 0 };
    for (const t of all) for (const s of Object.keys(c) as Status[]) if (matches(t, s)) c[s]++;
    return c;
  }, [all]);
  const total = filtered.reduce((s, t) => s + costMinor(t.cost), 0);

  return (
    <div>
      <PageHeader title="Tasks" subtitle={`${filtered.length} ${filtered.length === 1 ? "task" : "tasks"} · ${formatMoney(total)} spent in view`} />
      <Tabs<Status>
        className="mb-3"
        value={status}
        onChange={(v) => set("status", v === "all" ? "" : v)}
        items={[
          { id: "all", label: "All" },
          { id: "active", label: "Active", count: counts.active },
          { id: "waiting", label: "Waiting", count: counts.waiting },
          { id: "succeeded", label: "Succeeded" },
          { id: "failed", label: "Failed", count: counts.failed },
          { id: "cancelled", label: "Cancelled" },
        ]}
      />
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted" />
          <Input value={q} onChange={(e) => set("q", e.target.value)} placeholder="Search tasks…" aria-label="Search tasks" className="pl-9 pointer-coarse:h-11" />
        </div>
        <Select label="Agent" value={agent} onChange={(v) => set("agent", v)}>
          <option value="">All agents</option>
          {(agents ?? []).map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </Select>
        <Select label="Machine" value={node} onChange={(v) => set("node", v)}>
          <option value="">All machines</option>
          {(nodes ?? []).map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </Select>
        <Select label="Time range" value={range} onChange={(v) => set("range", v)}>
          <option value="24h">Last 24 h</option>
          <option value="7d">Last 7 days</option>
          <option value="30d">Last 30 days</option>
          <option value="all">All time</option>
        </Select>
        <label className="flex h-9 items-center gap-2 rounded-lg border border-border bg-surface px-2.5 text-[13px] text-fg-2">
          <input type="checkbox" checked={roots} onChange={(e) => set("roots", e.target.checked ? "" : "0")} className="accent-[var(--accent)]" />
          Top-level only
        </label>
      </div>
      <Card className="overflow-hidden">
        {list.isLoading && all.length === 0 ? (
          <SkeletonRows rows={8} />
        ) : list.isError && all.length === 0 ? (
          <ErrorState error={list.error} onRetry={() => list.refetch()} />
        ) : filtered.length === 0 ? (
          <EmptyState icon={<SearchX />} title="No tasks match" body="Try a different filter or time range." />
        ) : (
          <VirtualList className="divide-y divide-border" items={filtered} getKey={(t) => t.id} estimate={61} render={(t) => <TaskRow task={t} childCount={children.get(t.id)} />} />
        )}
      </Card>
    </div>
  );
}
