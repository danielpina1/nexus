import { useMemo } from "react";
import { Link } from "react-router";
import { ArrowRight, CheckCircle2, Coins, Cpu, Inbox as InboxIcon, Leaf, Loader2, Zap } from "lucide-react";
import { Card, CardHeader } from "../components/ui/Card";
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
import { useNodes } from "../lib/queries";
import { useEvents } from "../lib/store";
import { useDailyCosts, useTodayCost } from "../lib/costs";
import { costMinor, cx, formatDuration, formatMoney, formatTokens, isActive, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";

function greeting() {
  const h = new Date().getHours();
  return h < 5 ? "Good night" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
}

function Stat({ icon, label, value, sub, to, tone }: { icon: React.ReactNode; label: string; value: React.ReactNode; sub?: React.ReactNode; to?: string; tone?: string }) {
  const body = (
    <Card className={cx("h-full p-4 transition-colors", to && "hover:border-border-strong")}>
      <div className="flex items-center gap-2 text-xs font-medium text-muted">
        <span className={cx("[&_svg]:size-3.5", tone)}>{icon}</span>
        {label}
      </div>
      <div className="mt-2 text-2xl font-semibold tracking-tight text-fg tabular">{value}</div>
      {sub && <div className="mt-0.5 truncate text-xs text-muted">{sub}</div>}
    </Card>
  );
  return to ? <Link to={to}>{body}</Link> : body;
}

function MiniBars({ data }: { data: { day: number; minor: number }[] }) {
  const max = Math.max(1, ...data.map((d) => d.minor));
  return (
    <div className="flex h-8 items-end gap-[3px]" aria-hidden>
      {data.map((d, i) => (
        <div
          key={d.day}
          title={`${new Date(d.day).toLocaleDateString(undefined, { weekday: "short", day: "numeric" })}: ${formatMoney(d.minor)}`}
          className={cx("w-full rounded-t-[3px]", i === data.length - 1 ? "bg-accent" : "bg-accent/30")}
          style={{ height: `${Math.max(6, (d.minor / max) * 100)}%` }}
        />
      ))}
    </div>
  );
}

function LiveTaskCard({ task }: { task: Task }) {
  const agents = useAgentMap();
  const summary = useRunSummary(task.id);
  const now = useNow(1000);
  const waiting = task.state === TaskState.WAITING_APPROVAL || task.state === TaskState.WAITING_INPUT || task.state === TaskState.NEEDS_INPUT;
  const line = summary.lastLine.replace(/[*_`#>]/g, "").trim();
  const cost = summary.costMinor || costMinor(task.cost);
  return (
    <Link
      to={`/tasks/${task.id}`}
      className={cx(
        "group flex flex-col rounded-xl border bg-surface p-4 shadow-card transition-colors hover:border-border-strong",
        waiting ? "border-warn/50" : "border-border",
      )}
    >
      <div className="flex items-start gap-3">
        <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-semibold text-fg">{task.title || task.prompt}</div>
          <div className="truncate text-xs text-muted">
            {agentName(agents, task.agentId, task.workflowId)}
            {task.nodeId && ` · ${task.nodeId}`}
          </div>
        </div>
        <StateBadge state={task.state} />
      </div>
      <div className="mt-3 min-h-[2.6em] rounded-lg bg-surface-2 px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-fg-2">
        <div className={cx("line-clamp-2 break-words", task.state === TaskState.RUNNING && "caret")}>{line || (task.state === TaskState.RUNNING ? "Working" : task.summary || "Waiting…")}</div>
      </div>
      <div className="mt-2.5 flex items-center gap-3 text-xs text-muted tabular">
        <span>{formatDuration(now - tsMs(task.createdAt))}</span>
        {summary.toolCalls > 0 && <span>{summary.toolCalls} tools</span>}
        <span className="flex-1" />
        <span className="font-medium text-fg-2">{formatMoney(cost)}</span>
      </div>
    </Link>
  );
}

export default function Overview() {
  const running = useRunningTasks();
  const inbox = useInbox();
  const nodes = useNodes();
  const nodeStatus = useEvents((s) => s.nodeStatus);
  const connection = useEvents((s) => s.connection.status);
  const today = useTodayCost();
  const daily = useDailyCosts(14);
  const all = useAllTasks();
  const tasksById = useEvents((s) => s.tasks);

  // Show leaf work on the wall: hide workflow parents whose children are active.
  const wall = useMemo(
    () => running.filter((t) => !(t.workflowId && !t.agentId && running.some((c) => c.parentId === t.id))),
    [running],
  );
  const recent = useMemo(() => all.filter((t) => !isActive(t.state) && !t.parentId).slice(0, 6), [all]);
  const online = (nodes.data ?? []).filter((n) => (nodeStatus[n.id] ?? n.status)?.online).length;
  const loadingTasks = Object.keys(tasksById).length === 0 && (connection === "connecting" || connection === "idle");

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-end">
        <div className="flex-1">
          <h1 className="text-2xl font-semibold tracking-tight text-fg">{greeting()}, Daniel</h1>
          <p className="mt-1 text-[13px] text-muted">
            {new Date().toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" })} · {running.length} running · {inbox.length} need you
          </p>
        </div>
        <CommandBar className="w-full lg:max-w-xl" />
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat icon={<Loader2 />} tone="text-info" label="Running" value={running.length} sub={`${running.filter((t) => t.state === TaskState.RUNNING).length} active · ${running.filter((t) => t.state !== TaskState.RUNNING).length} waiting`} to="/tasks?status=active" />
        <Stat icon={<InboxIcon />} tone="text-danger" label="Needs you" value={inbox.length} sub={`${inbox.filter((i) => i.kind === "approval").length} approvals · ${inbox.filter((i) => i.kind === "question").length} questions · ${inbox.filter((i) => i.kind === "notice").length} notices`} to="/inbox" />
        <Link to="/costs">
          <Card className="h-full p-4 hover:border-border-strong">
            <div className="flex items-center gap-2 text-xs font-medium text-muted">
              <Coins className="size-3.5 text-warn" /> Today's cost
            </div>
            <div className="mt-2 flex items-end gap-3">
              <div className="min-w-0 flex-1">
                <div className="text-2xl font-semibold tracking-tight text-fg tabular">{formatMoney(today.cloud, today.currency)}</div>
                <div className="mt-0.5 flex items-center gap-1 truncate text-xs text-muted">
                  <Zap className="size-3" /> cloud · <Leaf className="size-3 text-ok" /> {formatTokens(today.localTokens)} local tok
                </div>
              </div>
              <div className="hidden w-20 sm:block">
                <MiniBars data={daily} />
              </div>
            </div>
          </Card>
        </Link>
        <Stat icon={<Cpu />} tone="text-ok" label="Machines" value={nodes.data ? `${online}/${nodes.data.length}` : "—"} sub="online" to="/machines" />
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="min-w-0 space-y-6 xl:col-span-2">
          <section>
            <div className="mb-3 flex items-center gap-2">
              <span className="relative flex size-2">
                {connection === "live" && <span className="absolute inline-flex size-full animate-ping rounded-full bg-ok opacity-60" />}
                <span className={cx("relative inline-flex size-2 rounded-full", connection === "live" ? "bg-ok" : "bg-muted")} />
              </span>
              <h2 className="text-[14px] font-semibold text-fg">Live</h2>
              <span className="text-xs text-muted">{wall.length} tasks</span>
              <span className="flex-1" />
              <Link to="/tasks?status=active" className="inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
                All tasks <ArrowRight className="size-3" />
              </Link>
            </div>
            {loadingTasks ? (
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                <Skeleton className="h-36" />
                <Skeleton className="h-36" />
              </div>
            ) : wall.length === 0 ? (
              <Card>
                <EmptyState compact icon={<CheckCircle2 />} title="Nothing running" body="Agents are idle. Ask Nexus for something above." />
              </Card>
            ) : (
              <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                {wall.map((t) => (
                  <LiveTaskCard key={t.id} task={t} />
                ))}
              </div>
            )}
          </section>

          <Card>
            <CardHeader title="Recent results" actions={<Link to="/tasks" className="text-xs text-muted hover:text-fg">View all</Link>} />
            {loadingTasks ? (
              <SkeletonRows rows={4} />
            ) : recent.length === 0 ? (
              <EmptyState compact title="No finished tasks yet" />
            ) : (
              <div className="divide-y divide-border">
                {recent.map((t) => (
                  <TaskRow key={t.id} task={t} compact />
                ))}
              </div>
            )}
          </Card>
        </div>

        <div className="min-w-0 space-y-6">
          <section>
            <div className="mb-3 flex items-center gap-2">
              <h2 className="text-[14px] font-semibold text-fg">Inbox</h2>
              {inbox.length > 0 && <span className="rounded-full bg-danger px-1.5 text-[11px] leading-5 font-semibold text-white tabular">{inbox.length}</span>}
              <span className="flex-1" />
              <Link to="/inbox" className="inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
                Open inbox <ArrowRight className="size-3" />
              </Link>
            </div>
            <div className="space-y-2.5">
              {inbox.length === 0 && (
                <Card>
                  <EmptyState compact icon={<InboxIcon />} title="Inbox zero" body="Nothing needs you right now." />
                </Card>
              )}
              {inbox.slice(0, 3).map((it) =>
                it.kind === "approval" ? (
                  <ApprovalCard key={it.id} approval={it.approval} compact />
                ) : it.kind === "question" ? (
                  <QuestionCard key={it.id} item={it.item} compact />
                ) : (
                  <NoticeCard key={it.id} item={it.item} compact />
                ),
              )}
              {inbox.length > 3 && (
                <Link to="/inbox" className="block rounded-lg py-1.5 text-center text-xs text-muted hover:bg-surface-2 hover:text-fg">
                  + {inbox.length - 3} more
                </Link>
              )}
            </div>
          </section>

          <Card>
            <CardHeader title="Machines" actions={<Link to="/machines" className="text-xs text-muted hover:text-fg">Details</Link>} />
            {nodes.isLoading ? (
              <SkeletonRows rows={3} />
            ) : (
              <div className="divide-y divide-border">
                {(nodes.data ?? []).map((n) => {
                  const st = nodeStatus[n.id] ?? n.status;
                  return (
                    <Link key={n.id} to={`/machines/${n.id}`} className="block px-4 py-3 hover:bg-surface-2">
                      <div className="mb-2 flex items-center gap-2">
                        <OnlineDot online={!!st?.online} />
                        <span className="text-[13px] font-medium text-fg">{n.name}</span>
                        <span className="text-xs text-muted">{n.os}</span>
                        <span className="flex-1" />
                        {(st?.activeRuns ?? 0) > 0 && <span className="text-xs text-info">{st?.activeRuns} running</span>}
                      </div>
                      <NodeMetrics status={st} />
                    </Link>
                  );
                })}
                {nodes.isError && <div className="px-4 py-6 text-center text-xs text-muted">Couldn't load machines.</div>}
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}
