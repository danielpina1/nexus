import { useMemo } from "react";
import { Link } from "react-router";
import { ArrowRight, CheckCircle2, Cpu } from "lucide-react";
import { Card } from "../components/ui/Card";
import { ErrorState } from "../components/ui/ErrorState";
import { prefetchPath } from "../app/registry";
import { prefetchTask } from "../lib/prefetch";
import { CommandBar } from "../components/CommandBar";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge } from "../components/StateBadge";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton, SkeletonRows } from "../components/ui/Skeleton";
import { NodeMetrics, OnlineDot } from "../components/NodeCard";
import { ApprovalCard, NoticeCard, QuestionCard } from "../components/InboxCards";
import { TaskRow } from "../components/TaskRow";
import { agentName, useAgentMap } from "../components/agentMeta";
import { useAllTasks, useInbox, useRunningTasks, useRunSummary } from "../lib/selectors";
import { useNodes, useTaskList } from "../lib/queries";
import { useEvents } from "../lib/store";
import { useDailyCosts, useTodayCost } from "../lib/costs";
import { costMinor, cx, formatDuration, formatMoney, formatTokens, isActive, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";

function greeting() {
  const h = new Date().getHours();
  return h < 5 ? "Good night" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
}

function Metric({ label, value, sub, to, tone, children }: { label: string; value: React.ReactNode; sub?: React.ReactNode; to: string; tone?: string; children?: React.ReactNode }) {
  return (
    <Link to={to} onMouseEnter={() => prefetchPath(to)} className="group flex min-w-0 flex-col justify-between gap-1 bg-surface px-4 py-3.5 transition-colors duration-150 hover:bg-surface-2 sm:px-5">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted">
        {tone && <span className={cx("size-1.5 rounded-full", tone)} aria-hidden />}
        {label}
      </div>
      <div className="flex items-end justify-between gap-3">
        <div className="min-w-0">
          <div className="font-heading text-[22px] leading-7 font-semibold text-fg tabular">{value}</div>
          {sub && <div className="truncate text-xs text-muted">{sub}</div>}
        </div>
        {children}
      </div>
    </Link>
  );
}

function MiniBars({ data }: { data: { day: number; minor: number }[] }) {
  const max = Math.max(1, ...data.map((d) => d.minor));
  return (
    <div className="hidden h-7 w-16 items-end gap-[2px] sm:flex" aria-hidden>
      {data.map((d, i) => (
        <div key={d.day} className={cx("w-full rounded-t-[2px]", i === data.length - 1 ? "bg-accent" : "bg-accent/25")} style={{ height: `${Math.max(8, (d.minor / max) * 100)}%` }} />
      ))}
    </div>
  );
}

function LiveTaskCard({ task }: { task: Task }) {
  const agents = useAgentMap();
  const summary = useRunSummary(task.id);
  const now = useNow(1000);
  const waiting = task.state === TaskState.WAITING_APPROVAL || task.state === TaskState.WAITING_INPUT || task.state === TaskState.NEEDS_INPUT;
  const line = summary.lastLine.replace(/[*_`#>[\]]/g, "").trim();
  const cost = summary.costMinor || costMinor(task.cost);
  return (
    <Link
      to={`/tasks/${task.id}`}
      onMouseEnter={() => prefetchTask(task.id)}
      className={cx(
        "flex flex-col rounded-xl border bg-surface p-4 shadow-card transition-colors duration-150 hover:border-border-strong",
        waiting ? "border-warn/50" : "border-border",
      )}
    >
      <div className="flex items-start gap-3">
        <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[14px] font-semibold text-fg">{task.title || task.prompt}</div>
          <div className="truncate text-xs text-muted">
            {agentName(agents, task.agentId, task.workflowId)}
            {task.nodeId && ` · ${task.nodeId}`}
          </div>
        </div>
        <StateBadge state={task.state} />
      </div>
      <div className="mt-3 min-h-[2.75em] rounded-lg bg-surface-2 px-3 py-2 font-mono text-[12px] leading-relaxed text-fg-2">
        <div className={cx("line-clamp-2 break-words", task.state === TaskState.RUNNING && "caret")}>
          {line || (waiting ? (task.state === TaskState.WAITING_APPROVAL ? "Waiting for your approval" : "Waiting for your answer") : task.summary || "Working…")}
        </div>
      </div>
      <div className="mt-2.5 flex items-center gap-3 text-xs text-muted tabular">
        <span>{formatDuration(now - tsMs(task.createdAt))}</span>
        {summary.toolCalls > 0 && <span>{summary.toolCalls} tools</span>}
        <span className="flex-1" />
        {cost > 0 && <span className="font-medium text-fg-2">{formatMoney(cost)}</span>}
      </div>
    </Link>
  );
}

function SectionTitle({ title, count, to, linkLabel, live }: { title: string; count?: number; to?: string; linkLabel?: string; live?: boolean }) {
  return (
    <div className="mb-3 flex items-center gap-2">
      {live && (
        <span className="relative flex size-2" aria-hidden>
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-ok opacity-50" />
          <span className="relative inline-flex size-2 rounded-full bg-ok" />
        </span>
      )}
      <h2 className="text-[15px] font-semibold text-fg">{title}</h2>
      {count != null && count > 0 && <span className="text-[13px] text-muted tabular">{count}</span>}
      <span className="flex-1" />
      {to && (
        <Link to={to} className="inline-flex min-h-8 items-center gap-1 text-[13px] text-muted hover:text-fg">
          {linkLabel} <ArrowRight className="size-3.5" aria-hidden />
        </Link>
      )}
    </div>
  );
}

export default function Overview() {
  const running = useRunningTasks();
  const inbox = useInbox();
  const nodes = useNodes();
  const nodeStatus = useEvents((s) => s.nodeStatus);
  const synced = useEvents((s) => s.synced);
  const today = useTodayCost();
  const daily = useDailyCosts(14);
  const all = useAllTasks();
  const tasksList = useTaskList({});

  // Show leaf work on the wall: hide workflow parents whose children are active.
  const wall = useMemo(() => running.filter((t) => !(t.workflowId && !t.agentId && running.some((c) => c.parentId === t.id))), [running]);
  const recent = useMemo(() => all.filter((t) => !isActive(t.state) && !t.parentId).slice(0, 6), [all]);
  const online = (nodes.data ?? []).filter((n) => (nodeStatus[n.id] ?? n.status)?.online).length;
  const loadingTasks = all.length === 0 && (tasksList.isLoading || !synced);
  const approvals = inbox.filter((i) => i.kind === "approval").length;
  const questions = inbox.filter((i) => i.kind === "question").length;

  return (
    <div className="space-y-8">
      <div className="space-y-5">
        <div>
          <h1 className="text-[24px] leading-tight font-semibold tracking-[-0.015em] text-fg">{greeting()}</h1>
          <p className="mt-1 text-[13px] text-muted">{new Date().toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" })}</p>
        </div>
        <CommandBar />
      </div>

      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-border bg-border shadow-card lg:grid-cols-4">
        <Metric label="Running" value={running.length} sub={running.length ? `${running.filter((t) => t.state === TaskState.RUNNING).length} active · ${running.length - running.filter((t) => t.state === TaskState.RUNNING).length} waiting` : "All quiet"} to="/tasks?status=active" tone={running.length ? "bg-info" : undefined} />
        <Metric label="Needs you" value={inbox.length} sub={inbox.length ? [approvals && `${approvals} approval${approvals > 1 ? "s" : ""}`, questions && `${questions} question${questions > 1 ? "s" : ""}`, inbox.length - approvals - questions > 0 && `${inbox.length - approvals - questions} notices`].filter(Boolean).join(" · ") : "Inbox zero"} to="/inbox" tone={inbox.length ? "bg-danger" : undefined} />
        <Metric label="Spent today" value={formatMoney(today.cloud, today.currency)} sub={`${formatTokens(today.localTokens)} local tokens`} to="/costs">
          <MiniBars data={daily} />
        </Metric>
        <Metric label="Machines" value={nodes.data ? `${online}/${nodes.data.length}` : "–"} sub={nodes.data ? (online === nodes.data.length ? "All online" : `${nodes.data.length - online} offline`) : "Loading…"} to="/machines" tone={nodes.data ? (online === nodes.data.length ? "bg-ok" : "bg-warn") : undefined} />
      </div>

      {inbox.length > 0 && (
        <section aria-label="Needs you">
          <SectionTitle title="Needs you" count={inbox.length} to="/inbox" linkLabel="Open inbox" />
          <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
            {inbox.slice(0, 4).map((it) =>
              it.kind === "approval" ? (
                <ApprovalCard key={it.id} approval={it.approval} compact />
              ) : it.kind === "question" ? (
                <QuestionCard key={it.id} item={it.item} compact />
              ) : (
                <NoticeCard key={it.id} item={it.item} compact />
              ),
            )}
          </div>
        </section>
      )}

      <div className="grid grid-cols-1 gap-8 xl:grid-cols-3">
        <div className="min-w-0 space-y-8 xl:col-span-2">
          <section aria-label="Running now">
            <SectionTitle title="Running now" count={wall.length} to="/tasks?status=active" linkLabel="All tasks" live={wall.length > 0} />
            {loadingTasks ? (
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                <Skeleton className="h-36" />
                <Skeleton className="h-36" />
              </div>
            ) : wall.length === 0 ? (
              <Card>
                <EmptyState compact icon={<CheckCircle2 />} title="Nothing running" body="Ask Nexus for something above, or press ⌘K." />
              </Card>
            ) : (
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                {wall.map((t) => (
                  <LiveTaskCard key={t.id} task={t} />
                ))}
              </div>
            )}
          </section>

          <section aria-label="Recent">
            <SectionTitle title="Recent" to="/tasks" linkLabel="View all" />
            <Card className="overflow-hidden">
              {loadingTasks ? (
                <SkeletonRows rows={4} />
              ) : tasksList.isError && all.length === 0 ? (
                <ErrorState compact error={tasksList.error} onRetry={() => tasksList.refetch()} />
              ) : recent.length === 0 ? (
                <EmptyState compact title="No finished tasks yet" body="Results show up here as soon as a run ends." />
              ) : (
                <div className="divide-y divide-border">
                  {recent.map((t) => (
                    <TaskRow key={t.id} task={t} compact />
                  ))}
                </div>
              )}
            </Card>
          </section>
        </div>

        <section aria-label="Machines" className="min-w-0">
          <SectionTitle title="Machines" to="/machines" linkLabel="Details" />
          <Card className="overflow-hidden">
            {nodes.isLoading ? (
              <SkeletonRows rows={2} />
            ) : nodes.isError ? (
              <ErrorState compact error={nodes.error} onRetry={() => nodes.refetch()} />
            ) : (nodes.data ?? []).length === 0 ? (
              <EmptyState compact icon={<Cpu />} title="No machines yet" body="Pair a node with this Hub to run agents on it." />
            ) : (
              <div className="divide-y divide-border">
                {(nodes.data ?? []).map((n) => {
                  const st = nodeStatus[n.id] ?? n.status;
                  return (
                    <Link key={n.id} to={`/machines/${n.id}`} className="block px-4 py-3.5 transition-colors duration-150 hover:bg-surface-2">
                      <div className="mb-2.5 flex items-center gap-2">
                        <OnlineDot online={!!st?.online} />
                        <span className="text-[13.5px] font-medium text-fg">{n.name}</span>
                        <span className="text-xs text-muted">{n.os}</span>
                        <span className="flex-1" />
                        {(st?.activeRuns ?? 0) > 0 && <span className="text-xs text-info">{st?.activeRuns} running</span>}
                        {!st?.online && <span className="text-xs text-muted">offline</span>}
                      </div>
                      <NodeMetrics status={st} />
                    </Link>
                  );
                })}
              </div>
            )}
          </Card>
        </section>
      </div>
    </div>
  );
}
