import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, ChevronRight, File, Folder, FolderOpen, Home, Power, RefreshCw } from "lucide-react";
import { api } from "../lib/api";
import { qk } from "../lib/queries";
import { useEvents } from "../lib/store";
import { useAllTasks } from "../lib/selectors";
import { toast } from "../lib/toast";
import { Card, CardHeader } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { Tabs } from "../components/ui/Tabs";
import { Skeleton, SkeletonRows } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { NodeMetrics, OnlineDot } from "../components/NodeCard";
import { TaskRow } from "../components/TaskRow";
import { formatBytes, formatDateTime, isActive, timeAgo, tsDate, tsMs } from "../lib/format";

function FileBrowser({ nodeId }: { nodeId: string }) {
  const [path, setPath] = useState("");
  const q = useQuery({
    queryKey: qk.files(nodeId, path),
    queryFn: async () => (await api().nodes.listFiles({ nodeId, path })).entries,
  });
  const entries = q.data ?? [];
  // Derive the current directory from the first entry when the server picked the default (home).
  const cwd = path || (entries[0]?.path ? entries[0].path.slice(0, entries[0].path.lastIndexOf("/")) || "/" : "");
  const parts = cwd.split("/").filter(Boolean);
  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center gap-1 border-b border-border px-3 py-2 text-[13px]">
        <button onClick={() => setPath("")} className="rounded p-1 text-muted hover:bg-surface-2 hover:text-fg" aria-label="Home">
          <Home className="size-3.5" />
        </button>
        {parts.map((p, i) => {
          const to = "/" + parts.slice(0, i + 1).join("/");
          return (
            <span key={to} className="flex items-center gap-1">
              <ChevronRight className="size-3 text-muted" />
              <button onClick={() => setPath(to)} className={i === parts.length - 1 ? "font-medium text-fg" : "text-muted hover:text-fg"}>
                {p}
              </button>
            </span>
          );
        })}
        <span className="flex-1" />
        <Button size="xs" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => q.refetch()} loading={q.isFetching}>
          Refresh
        </Button>
      </div>
      {q.isLoading ? (
        <SkeletonRows rows={5} />
      ) : q.isError ? (
        <ErrorState compact error={q.error} onRetry={() => q.refetch()} />
      ) : entries.length === 0 ? (
        <EmptyState compact icon={<FolderOpen />} title="Empty folder" />
      ) : (
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b border-border text-left text-xs text-muted">
              <th className="px-4 py-2 font-medium">Name</th>
              <th className="hidden px-4 py-2 text-right font-medium sm:table-cell">Size</th>
              <th className="hidden px-4 py-2 text-right font-medium md:table-cell">Modified</th>
            </tr>
          </thead>
          <tbody>
            {cwd !== "/" && parts.length > 0 && (
              <tr className="border-b border-border/60 hover:bg-surface-2">
                <td colSpan={3} className="px-4 py-1.5">
                  <button onClick={() => setPath("/" + parts.slice(0, -1).join("/"))} className="flex items-center gap-2 text-muted hover:text-fg">
                    <Folder className="size-4" /> ..
                  </button>
                </td>
              </tr>
            )}
            {entries.map((e) => (
              <tr key={e.path} data-testid="file-row" className="border-b border-border/60 last:border-0 hover:bg-surface-2">
                <td className="px-4 py-1.5">
                  {e.isDir ? (
                    <button onClick={() => setPath(e.path)} className="flex items-center gap-2 font-medium text-fg">
                      <Folder className="size-4 text-accent" />
                      {e.name}
                    </button>
                  ) : (
                    <span className="flex items-center gap-2 text-fg-2">
                      <File className="size-4 text-muted" />
                      {e.name}
                    </span>
                  )}
                </td>
                <td className="hidden px-4 py-1.5 text-right text-muted tabular sm:table-cell">{e.isDir ? "—" : formatBytes(e.sizeBytes)}</td>
                <td className="hidden px-4 py-1.5 text-right text-muted tabular md:table-cell">{formatDateTime(tsDate(e.modifiedAt))}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

export default function MachineDetail() {
  const { id = "" } = useParams();
  const q = useQuery({ queryKey: qk.node(id), queryFn: async () => (await api().nodes.getNode({ id })).node });
  const live = useEvents((s) => s.nodeStatus[id]);
  const tasks = useAllTasks();
  const [tab, setTab] = useState<"overview" | "files" | "runs">("overview");
  const wake = useMutation({
    mutationFn: async () => api().nodes.wakeNode({ id }),
    onSuccess: () => toast.success("Wake-on-LAN sent"),
    onError: (e) => toast.error("Wake failed", e),
  });

  if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  const node = q.data;
  if (!node)
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64" />
      </div>
    );
  const st = live ?? node.status;
  const runs = tasks.filter((t) => t.nodeId === id);
  const activeRuns = runs.filter((t) => isActive(t.state));

  return (
    <div>
      <Link to="/machines" className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ArrowLeft className="size-3.5" /> Machines
      </Link>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <div className="flex size-11 items-center justify-center rounded-xl bg-surface-2 text-fg-2">
          <OnlineDot online={!!st?.online} className="size-2.5" />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight text-fg">{node.name}</h1>
          <div className="text-xs text-muted">
            {node.os} · {node.arch} · paired {timeAgo(tsMs(node.pairedAt))} · {st?.online ? `seen ${timeAgo(tsMs(st.lastSeen))}` : "offline"}
          </div>
        </div>
        <div className="flex flex-wrap gap-1">
          {node.tags.map((t) => (
            <Badge key={t}>#{t}</Badge>
          ))}
        </div>
        <Button icon={<Power className="size-3.5" />} loading={wake.isPending} onClick={() => wake.mutate()} disabled={st?.online}>
          Wake
        </Button>
      </div>

      <Tabs<"overview" | "files" | "runs">
        className="mb-4"
        value={tab}
        onChange={setTab}
        items={[
          { id: "overview", label: "Overview" },
          { id: "files", label: "Files" },
          { id: "runs", label: "Runs", count: activeRuns.length },
        ]}
      />

      {tab === "overview" && (
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
          <Card className="p-5">
            <div className="mb-4 text-[13px] font-semibold text-fg">Health</div>
            <NodeMetrics status={st} />
            <div className="mt-4 grid grid-cols-2 gap-3 text-[13px]">
              <div>
                <div className="text-xs text-muted">Active runs</div>
                <div className="font-heading text-lg font-semibold text-fg tabular">{st?.activeRuns ?? 0}</div>
              </div>
              {(st?.batteryPercent ?? 0) > 0 && (
                <div>
                  <div className="text-xs text-muted">Battery</div>
                  <div className="font-heading text-lg font-semibold text-fg tabular">{Math.round(st!.batteryPercent)}%</div>
                </div>
              )}
            </div>
          </Card>
          <Card>
            <CardHeader title="Runtimes" subtitle="Installed agent runtimes reported by the node" />
            <div className="divide-y divide-border">
              {node.runtimes.map((r) => (
                <div key={r.name} className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
                  <span className="font-medium text-fg">{r.name}</span>
                  <span className="font-mono text-xs text-muted">{r.version}</span>
                  <span className="flex-1" />
                  <Badge tone={r.available ? "ok" : "warn"} dot>
                    {r.available ? "available" : "unavailable"}
                  </Badge>
                </div>
              ))}
            </div>
          </Card>
          <Card className="lg:col-span-2">
            <CardHeader title="Running here" />
            {activeRuns.length === 0 ? (
              <div className="p-6 text-center text-xs text-muted">Nothing running on {node.name}.</div>
            ) : (
              <div className="divide-y divide-border">
                {activeRuns.map((t) => (
                  <TaskRow key={t.id} task={t} />
                ))}
              </div>
            )}
          </Card>
        </div>
      )}
      {tab === "files" && <FileBrowser nodeId={id} />}
      {tab === "runs" && (
        <Card className="overflow-hidden">
          {runs.length === 0 ? (
            <EmptyState compact title="No runs on this machine" />
          ) : (
            <div className="divide-y divide-border">
              {runs.slice(0, 50).map((t) => (
                <TaskRow key={t.id} task={t} />
              ))}
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
