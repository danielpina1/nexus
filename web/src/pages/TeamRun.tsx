import { memo, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import {
  AlertOctagon,
  ArrowLeft,
  ArrowRightLeft,
  CheckCircle2,
  Clock,
  Coins,
  ExternalLink,
  Flag,
  GitBranch,
  Hand,
  Hourglass,
  ListTree,
  MessageSquare,
  NotebookPen,
  Play,
  RefreshCw,
  ScrollText,
  Send,
  Sparkles,
  SquareStack,
  StickyNote,
  Users,
  XCircle,
} from "lucide-react";
import { useTreeHistory } from "../lib/events";
import { useEvents, isPendingApproval } from "../lib/store";
import { useCancelTask } from "../lib/queries";
import {
  mergeBoard,
  mergeMessages,
  runTotals,
  strategyLabel,
  useSendTeamMessage,
  useTeam,
  useTeamEvents,
  useTeamRunQuery,
  useTreeTasks,
  type BoardItem,
  type MessageItem,
  type TeamEventItem,
} from "../lib/teams";
import { costMinor, cx, formatClock, formatDuration, formatMoney, isActive, taskStateLabel, timeAgo, tsMs, type Tone } from "../lib/format";
import { useNow } from "../lib/useNow";
import { Card, CardHeader } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { buttonClass } from "../components/ui/buttonStyles";
import { ConfirmButton } from "../components/ui/ConfirmButton";
import { Input } from "../components/ui/Input";
import { LimitMeter } from "../components/ui/LimitMeter";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { VirtualList } from "../components/VirtualList";
import { Markdown } from "../components/Markdown";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge } from "../components/StateBadge";
import { ApprovalCard } from "../components/InboxCards";
import { TeamIcon } from "../components/team/TeamIcon";
import { agentName, useAgentMap } from "../components/agentMeta";
import { TeamEventKind } from "../gen/nexus/v1/event_pb";
import type { Agent } from "../gen/nexus/v1/agent_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { ApprovalState, type Approval } from "../gen/nexus/v1/approval_pb";

const WAITING = new Set([TaskState.WAITING_APPROVAL, TaskState.WAITING_INPUT, TaskState.NEEDS_INPUT]);

const PHASE: Record<string, { label: string; tone: Tone }> = {
  waiting_slot: { label: "waiting for a slot", tone: "neutral" },
  strategy: { label: "running strategy", tone: "info" },
  reviewing: { label: "critic reviewing", tone: "accent" },
  revising: { label: "revising", tone: "accent" },
  repairing: { label: "repairing output", tone: "warn" },
};

function phaseBadge(phase: string) {
  if (!phase) return null;
  const p = PHASE[phase] ?? { label: phase.replace(/_/g, " "), tone: "neutral" as Tone };
  return (
    <Badge tone={p.tone} title={`Phase: ${phase}`}>
      {phase === "waiting_slot" ? <Hourglass className="size-3" aria-hidden /> : <RefreshCw className="size-3" aria-hidden />}
      {p.label}
    </Badge>
  );
}

function runtimeOf(t: Task, agents: Record<string, Agent>): string {
  if (t.agentId.startsWith("eph:")) return "ephemeral";
  const a = agents[t.agentId];
  return t.cost?.runtime || a?.runtime || "";
}

// ---- tree ---------------------------------------------------------------------------------------

const TreeNode = memo(function TreeNode({
  task,
  kids,
  agents,
  now,
  depth,
}: {
  task: Task;
  kids: Map<string, Task[]>;
  agents: Record<string, Agent>;
  now: number;
  depth: number;
}) {
  const children = kids.get(task.id) ?? [];
  const active = isActive(task.state);
  const dur = (active ? now : tsMs(task.updatedAt)) - tsMs(task.createdAt);
  const rt = runtimeOf(task, agents);
  const role = task.role || (depth === 0 ? "orchestrator" : "sub-task");
  return (
    <li className={cx("relative", depth > 0 && "pl-5 before:absolute before:top-[26px] before:left-0 before:h-px before:w-4 before:bg-border-strong")}>
      <Link
        to={`/tasks/${task.id}`}
        data-testid="team-tree-node"
        className={cx(
          "group flex min-h-[52px] items-start gap-2.5 rounded-lg border px-3 py-2 transition-colors duration-150 hover:border-border-strong hover:bg-surface-2",
          WAITING.has(task.state) ? "border-warn/40" : "border-border",
          depth === 0 ? "bg-surface-2/60" : "bg-surface",
        )}
      >
        <AgentIcon agentId={task.agentId} icon={agents[task.agentId]?.icon} size="sm" className="mt-0.5" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={depth === 0 ? "accent" : "neutral"} className="font-semibold">
              {role}
            </Badge>
            <span className="min-w-0 truncate text-[13px] font-medium text-fg">{task.title || task.prompt}</span>
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11.5px] text-muted tabular">
            <span className="text-fg-2">{agentName(agents, task.agentId)}</span>
            {rt && (
              <>
                <span aria-hidden>·</span>
                <span>{rt}</span>
              </>
            )}
            {task.nodeId && (
              <>
                <span aria-hidden>·</span>
                <span>{task.nodeId}</span>
              </>
            )}
            <span aria-hidden>·</span>
            <span title="Time">{formatDuration(dur)}</span>
            <span aria-hidden>·</span>
            <span title="Cost">{formatMoney(costMinor(task.cost), task.cost?.amount?.currency)}</span>
          </div>
          {task.summary && !active && <p className="mt-1 line-clamp-2 text-xs text-fg-2">{task.summary}</p>}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <StateBadge state={task.state} />
          {phaseBadge(task.phase)}
        </div>
      </Link>
      {children.length > 0 && (
        <ul className="relative mt-2 ml-[22px] space-y-2 border-l border-border-strong pb-1" aria-label={`Sub-tasks of ${role}`}>
          {children.map((c) => (
            <TreeNode key={c.id} task={c} kids={kids} agents={agents} now={now} depth={depth + 1} />
          ))}
        </ul>
      )}
    </li>
  );
});

function TaskTree({ root, tree, now }: { root: Task; tree: Task[]; now: number }) {
  const agents = useAgentMap();
  const kids = useMemo(() => {
    const m = new Map<string, Task[]>();
    const ids = new Set(tree.map((t) => t.id));
    for (const t of tree) {
      if (t.id === root.id) continue;
      // Orphans (parent not loaded yet) hang off the root.
      const p = t.parentId && ids.has(t.parentId) ? t.parentId : root.id;
      const l = m.get(p);
      if (l) l.push(t);
      else m.set(p, [t]);
    }
    return m;
  }, [tree, root.id]);
  return (
    <ul className="p-3" aria-label="Task tree">
      <TreeNode task={root} kids={kids} agents={agents} now={now} depth={0} />
    </ul>
  );
}

// ---- timeline -----------------------------------------------------------------------------------

const KIND: Record<TeamEventKind, { label: string; icon: React.ReactNode; tone: Tone }> = {
  [TeamEventKind.UNSPECIFIED]: { label: "event", icon: <Sparkles />, tone: "neutral" },
  [TeamEventKind.STARTED]: { label: "started", icon: <Play />, tone: "info" },
  [TeamEventKind.DELEGATED]: { label: "delegated", icon: <GitBranch />, tone: "accent" },
  [TeamEventKind.SPAWNED]: { label: "spawned", icon: <Sparkles />, tone: "accent" },
  [TeamEventKind.MEMBER_FINISHED]: { label: "finished", icon: <CheckCircle2 />, tone: "ok" },
  [TeamEventKind.BLACKBOARD_UPDATED]: { label: "blackboard", icon: <StickyNote />, tone: "neutral" },
  [TeamEventKind.MESSAGE_SENT]: { label: "message", icon: <MessageSquare />, tone: "info" },
  [TeamEventKind.REPORTED]: { label: "reported", icon: <NotebookPen />, tone: "ok" },
  [TeamEventKind.AWAITING]: { label: "awaiting", icon: <Hourglass />, tone: "neutral" },
  [TeamEventKind.STRATEGY_STEP]: { label: "strategy", icon: <ArrowRightLeft />, tone: "info" },
  [TeamEventKind.REVIEW]: { label: "review", icon: <ScrollText />, tone: "accent" },
  [TeamEventKind.LIMIT_REACHED]: { label: "limit reached", icon: <AlertOctagon />, tone: "danger" },
  [TeamEventKind.APPROVAL_PENDING]: { label: "approval", icon: <Hand />, tone: "warn" },
  [TeamEventKind.FINISHED]: { label: "run ended", icon: <Flag />, tone: "ok" },
};

const toneText: Record<Tone, string> = { neutral: "text-muted", accent: "text-accent", ok: "text-ok", warn: "text-warn", danger: "text-danger", info: "text-info" };

function describe(ev: TeamEventItem["ev"]): string {
  const role = ev.role || "orchestrator";
  const state = typeof ev.data?.state === "string" ? ev.data.state.replace(/^TASK_STATE_/, "").toLowerCase() : "";
  switch (ev.kind) {
    case TeamEventKind.STARTED:
      return `Team run started${ev.text ? ` — ${ev.text}` : ""}`;
    case TeamEventKind.DELEGATED:
      return `→ ${role}: ${ev.text}`;
    case TeamEventKind.SPAWNED:
      return `Spawned specialist ${role}: ${ev.text}`;
    case TeamEventKind.MEMBER_FINISHED:
      return `${role} ${state || "finished"}${ev.text ? ` — ${ev.text}` : ""}`;
    case TeamEventKind.BLACKBOARD_UPDATED:
      return `${role} ${ev.data?.append ? "appended to" : "wrote"} “${ev.key}”`;
    case TeamEventKind.MESSAGE_SENT:
      return `${String(ev.data?.from_role ?? role)} → ${String(ev.data?.to ?? "")}: ${ev.text}`;
    case TeamEventKind.REPORTED:
      return `${role} reported: ${ev.text}`;
    case TeamEventKind.AWAITING:
      return `${role} ${ev.text || "is waiting for members"}`;
    case TeamEventKind.REVIEW:
      return `Critic${ev.data?.approved === true ? " approved" : ev.data?.approved === false ? " asked for changes" : ""}: ${ev.text}`;
    case TeamEventKind.APPROVAL_PENDING:
      return `${role} waits for your approval: ${ev.text}`;
    case TeamEventKind.FINISHED:
      return `Run ${state || "ended"}${ev.text ? ` — ${ev.text}` : ""}`;
    default:
      return ev.text;
  }
}

function Timeline({ events }: { events: TeamEventItem[] }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [filter, setFilter] = useState<"all" | "important">("all");
  const list = useMemo(() => {
    const l = filter === "all" ? events : events.filter((e) => e.ev.kind !== TeamEventKind.BLACKBOARD_UPDATED && e.ev.kind !== TeamEventKind.AWAITING);
    return l.slice().reverse();
  }, [events, filter]);
  return (
    <Card className="min-w-0 overflow-hidden">
      <CardHeader
        icon={<ListTree className="size-4" />}
        title="Live timeline"
        subtitle={`${events.length} team events · newest first`}
        actions={
          <label className="flex min-h-8 items-center gap-1.5 text-xs text-muted">
            <input type="checkbox" checked={filter === "important"} onChange={(e) => setFilter(e.target.checked ? "important" : "all")} className="size-4 accent-[var(--accent)]" />
            Key events only
          </label>
        }
      />
      {list.length === 0 ? (
        <EmptyState compact icon={<ListTree />} title="No team events yet" body="Delegations, results, messages and limits appear here as they happen." />
      ) : (
        <div ref={scrollRef} className="max-h-[520px] overflow-y-auto scrollbar-thin" aria-live="polite" aria-relevant="additions">
          <VirtualList
            items={list}
            scrollRef={scrollRef}
            estimate={44}
            getKey={(e) => e.seq.toString()}
            className="divide-y divide-border/70"
            render={({ ev, at }) => {
              const k = KIND[ev.kind] ?? KIND[TeamEventKind.UNSPECIFIED];
              const body = (
                <>
                  <span className={cx("mt-0.5 shrink-0 [&_svg]:size-3.5", toneText[k.tone])} aria-hidden>
                    {k.icon}
                  </span>
                  <span className="shrink-0 pt-px text-[11.5px] whitespace-nowrap text-muted tabular">{formatClock(new Date(at))}</span>
                  <span className="min-w-0 flex-1">
                    <span className={cx("mr-1.5 text-[11px] font-semibold tracking-wide uppercase", toneText[k.tone])}>{k.label}</span>
                    <span className="text-[13px] break-words text-fg-2">{describe(ev)}</span>
                  </span>
                </>
              );
              return ev.taskId ? (
                <Link to={`/tasks/${ev.taskId}`} className="flex min-h-11 items-start gap-2.5 px-4 py-2 hover:bg-surface-2">
                  {body}
                </Link>
              ) : (
                <div className="flex min-h-11 items-start gap-2.5 px-4 py-2">{body}</div>
              );
            }}
          />
        </div>
      )}
    </Card>
  );
}

// ---- blackboard & messages ----------------------------------------------------------------------

function BoardEntry({ item, now }: { item: BoardItem; now: number }) {
  const [open, setOpen] = useState(false);
  const long = item.text.length > 220 || item.text.split("\n").length > 6;
  return (
    <li className="px-4 py-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="font-mono text-[12.5px] font-semibold text-fg">{item.key}</span>
        {item.role && <Badge>{item.role}</Badge>}
        <span className="text-[11px] text-muted tabular">
          v{item.version} · {timeAgo(item.at, now)}
        </span>
      </div>
      <pre className={cx("mt-1 overflow-hidden font-sans text-[12.5px] whitespace-pre-wrap text-fg-2", !open && long && "line-clamp-4")}>{item.text}</pre>
      {long && (
        <button onClick={() => setOpen((v) => !v)} className="mt-0.5 min-h-7 text-xs text-accent hover:underline" aria-expanded={open}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </li>
  );
}

function Blackboard({ items, enabled }: { items: BoardItem[]; enabled: boolean }) {
  const now = useNow(15_000);
  return (
    <Card className="min-w-0 overflow-hidden">
      <CardHeader icon={<StickyNote className="size-4" />} title="Blackboard" subtitle={enabled ? `${items.length} shared ${items.length === 1 ? "entry" : "entries"}` : "Off for this team"} />
      {items.length === 0 ? (
        <EmptyState compact icon={<StickyNote />} title="Nothing written yet" body="Members share plans, findings and drafts here." />
      ) : (
        <ul className="max-h-[420px] divide-y divide-border overflow-y-auto scrollbar-thin">
          {items.map((b) => (
            <BoardEntry key={b.key} item={b} now={now} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function Messages({ rootId, items, roles, active }: { rootId: string; items: MessageItem[]; roles: string[]; active: boolean }) {
  const send = useSendTeamMessage(rootId);
  const [to, setTo] = useState("orchestrator");
  const [text, setText] = useState("");
  const now = useNow(15_000);
  const listRef = useRef<HTMLUListElement>(null);
  const submit = () => {
    const t = text.trim();
    if (!t) return;
    send.mutate({ to, text: t }, { onSuccess: () => setText("") });
  };
  return (
    <Card className="min-w-0 overflow-hidden">
      <CardHeader icon={<MessageSquare className="size-4" />} title="Team messages" subtitle={`${items.length} ${items.length === 1 ? "message" : "messages"}`} />
      {items.length === 0 ? (
        <EmptyState compact icon={<MessageSquare />} title="No messages yet" body="Members talk to each other here; you can join in." />
      ) : (
        <ul ref={listRef} className="max-h-[360px] space-y-2 overflow-y-auto p-3 scrollbar-thin">
          {items.map((m) => {
            const mine = m.from === "user";
            return (
              <li key={m.id} className={cx("flex", mine && "justify-end")}>
                <div className={cx("max-w-[92%] rounded-xl px-3 py-2", mine ? "bg-accent-soft" : "bg-surface-2")}>
                  <div className="mb-0.5 flex flex-wrap items-center gap-1 text-[11px] text-muted">
                    <span className="font-semibold text-fg-2">{mine ? "You" : m.from}</span>
                    <span aria-hidden>→</span>
                    <span>{m.to || "orchestrator"}</span>
                    <span>· {timeAgo(m.at, now)}</span>
                  </div>
                  <p className="text-[13px] break-words whitespace-pre-wrap text-fg">{m.text}</p>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      <form
        className="flex flex-wrap items-center gap-2 border-t border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <select
          aria-label="Send to"
          value={to}
          onChange={(e) => setTo(e.target.value)}
          disabled={!active}
          className="h-9 rounded-lg border border-border-input bg-surface px-2 text-base text-fg disabled:opacity-60 sm:text-[13px] pointer-coarse:h-11"
        >
          <option value="orchestrator">to orchestrator</option>
          <option value="all">to everyone</option>
          {roles.map((r) => (
            <option key={r} value={r}>
              to {r}
            </option>
          ))}
        </select>
        <Input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={active ? "Message the team…" : "The run has ended"}
          aria-label="Message to the team"
          disabled={!active}
          data-testid="team-message-input"
          className="min-w-0 flex-1 basis-40 pointer-coarse:h-11"
        />
        <Button type="submit" size="icon" variant="primary" aria-label="Send message" loading={send.isPending} disabled={!active || !text.trim()}>
          {!send.isPending && <Send className="size-3.5" />}
        </Button>
      </form>
    </Card>
  );
}

// ---- page ---------------------------------------------------------------------------------------

export default function TeamRun() {
  const { id: rootId = "" } = useParams();
  useTreeHistory(rootId);
  const runQ = useTeamRunQuery(rootId);
  const tree = useTreeTasks(rootId);
  const root = tree.find((t) => t.id === rootId) ?? runQ.data?.root;
  const events = useTeamEvents(rootId);
  const teamQ = useTeam(!runQ.data?.team && root?.teamId ? root.teamId : undefined);
  const team = runQ.data?.team ?? teamQ.data?.team;
  const approvalsAll = useEvents((s) => s.approvals);
  const cancel = useCancelTask();
  const active = root ? isActive(root.state) : false;
  const now = useNow(active ? 1000 : 60_000);

  const ids = useMemo(() => new Set(tree.map((t) => t.id)), [tree]);
  const approvals = useMemo(() => {
    const list: Approval[] = Object.values(approvalsAll).filter((a) => a.rootTaskId === rootId || ids.has(a.taskId));
    return { pending: list.filter((a) => isPendingApproval(a, now)), decided: list.filter((a) => !isPendingApproval(a, now)) };
  }, [approvalsAll, rootId, ids, now]);
  const board = useMemo(() => mergeBoard(runQ.data?.blackboard, events), [runQ.data?.blackboard, events]);
  const messages = useMemo(() => mergeMessages(runQ.data?.messages, events), [runQ.data?.messages, events]);
  const totals = runTotals(runQ.data, tree, root);
  const limits = runQ.data?.limits ?? team?.limits;
  const roles = useMemo(() => [...new Set([...(team?.members.map((m) => m.role) ?? []), ...tree.filter((t) => t.id !== rootId && t.role).map((t) => t.role)])], [team, tree, rootId]);
  const stopped = runQ.data?.stoppedReason ?? "";

  if (!root) {
    if (runQ.isError) return <ErrorState error={runQ.error} onRetry={() => runQ.refetch()} />;
    return (
      <div className="space-y-4">
        <Skeleton className="h-9 w-80" />
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-[92px]" />
          ))}
        </div>
        <Skeleton className="h-[420px]" />
      </div>
    );
  }

  const start = totals.startedAt;
  const elapsed = (totals.endedAt || now) - start;
  const deadlineMs = tsMs(runQ.data?.deadline);
  const timeLimit = deadlineMs ? deadlineMs - start : limits?.timeoutSeconds ? Number(limits.timeoutSeconds) * 1000 : 0;
  const maxCost = limits?.maxCost ? Number(limits.maxCost.minorUnits) : 0;
  const result = !active && (root.summary || "");

  return (
    <div data-testid="team-run">
      <Link
        to={root.teamId ? `/teams/${root.teamId}` : "/teams"}
        className="mb-3 inline-flex min-h-8 items-center gap-1 text-[13px] text-muted hover:text-fg"
      >
        <ArrowLeft className="size-3.5" aria-hidden /> {team?.name ?? "Teams"}
      </Link>

      <div className="mb-5 flex flex-wrap items-start gap-3">
        <TeamIcon id={root.teamId || "tree"} icon={team?.icon} size="lg" />
        <div className="min-w-0 flex-1 basis-60">
          <h1 className="text-[22px] leading-tight font-semibold tracking-[-0.01em] break-words text-fg">{root.title || root.prompt}</h1>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-muted">
            <StateBadge state={root.state} />
            {phaseBadge(root.phase)}
            {active && (
              <Badge tone="info" dot pulse>
                live
              </Badge>
            )}
            {team && <Badge tone="accent">{strategyLabel[team.strategy]}</Badge>}
            <span>started {timeAgo(start, now)}</span>
            {totals.active + totals.queued > 0 && (
              <span>
                · {totals.active} active{totals.queued ? ` · ${totals.queued} queued` : ""}
              </span>
            )}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Link to={`/tasks/${root.id}`} className={buttonClass("ghost")}>
            <ExternalLink className="size-3.5" aria-hidden /> Root task
          </Link>
          {active && (
            <ConfirmButton
              icon={<XCircle className="size-3.5" />}
              label="Cancel run"
              confirmLabel="Confirm cancel"
              loading={cancel.isPending}
              onConfirm={() => cancel.mutate(root.id)}
              data-testid="team-run-cancel"
            />
          )}
        </div>
      </div>

      {stopped && (
        <div role="alert" className="mb-5 flex items-start gap-2 rounded-xl border border-danger/40 bg-danger-soft px-4 py-3 text-[13px] text-danger">
          <AlertOctagon className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>
            <b>Stopped:</b> {stopped}
          </span>
        </div>
      )}

      <section aria-label="Totals against limits" className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <LimitMeter
          label="Tasks"
          icon={<SquareStack />}
          value={totals.tasks}
          max={limits?.maxTasks || undefined}
          display={totals.tasks}
          limitDisplay={limits?.maxTasks || "∞"}
          hint={`${totals.active} active · ${totals.queued} queued`}
        />
        <LimitMeter label="Runs" icon={<RefreshCw />} value={totals.runs} max={limits?.maxRuns || undefined} display={totals.runs} limitDisplay={limits?.maxRuns || "∞"} hint="retries, reviews and repairs included" />
        <LimitMeter
          label="Cost"
          icon={<Coins />}
          value={totals.costMinor}
          max={maxCost || undefined}
          display={formatMoney(totals.costMinor, totals.currency)}
          limitDisplay={maxCost ? formatMoney(maxCost, limits?.maxCost?.currency) : "no budget"}
          hint={maxCost ? `${formatMoney(Math.max(0, maxCost - totals.costMinor), totals.currency)} left` : "LLM spend of the whole tree"}
        />
        <LimitMeter
          label="Time"
          icon={<Clock />}
          value={elapsed}
          max={timeLimit || undefined}
          display={formatDuration(elapsed)}
          limitDisplay={timeLimit ? formatDuration(timeLimit) : "no deadline"}
          hint={active && deadlineMs ? `deadline ${formatClock(new Date(deadlineMs))}` : active ? "running" : "ended"}
        />
      </section>

      <div className="flex flex-col gap-5 lg:grid lg:grid-cols-[minmax(0,1fr)_380px] lg:items-start">
        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-5">
          {approvals.pending.length > 0 && (
            <section aria-label="Pending approvals" className="order-1 space-y-3 lg:order-none">
              <h2 className="flex items-center gap-2 text-[13px] font-semibold text-warn">
                <Hand className="size-4" aria-hidden /> {approvals.pending.length} {approvals.pending.length === 1 ? "approval waits" : "approvals wait"} for you
              </h2>
              {approvals.pending.map((a) => (
                <ApprovalCard key={a.id} approval={a} />
              ))}
            </section>
          )}
          {result && (
            <Card className="order-2 p-4 lg:order-none">
              <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Result</div>
              <Markdown className="text-[13.5px]">{result}</Markdown>
            </Card>
          )}
          <Card className="order-3 min-w-0 overflow-hidden lg:order-none">
            <CardHeader
              icon={<Users className="size-4" />}
              title="Task tree"
              subtitle={`${tree.length} ${tree.length === 1 ? "task" : "tasks"} · ${taskStateLabel[root.state].toLowerCase()}`}
            />
            <TaskTree root={root} tree={tree.length ? tree : [root]} now={now} />
          </Card>
          <div className="order-6 lg:order-none">
            <Timeline events={events} />
          </div>
        </div>
        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-5">
          <div className="order-4 lg:order-none">
            <Blackboard items={board} enabled={team?.blackboard ?? true} />
          </div>
          <div className="order-5 lg:order-none">
            <Messages rootId={rootId} items={messages} roles={roles} active={active} />
          </div>
          {approvals.decided.length > 0 && (
            <Card className="order-7 lg:order-none">
              <CardHeader icon={<Hand className="size-4" />} title="Decided approvals" subtitle={`${approvals.decided.length} in this run`} />
              <ul className="divide-y divide-border">
                {approvals.decided.map((a) => (
                  <li key={a.id} className="flex items-center gap-2 px-4 py-2 text-[13px]">
                    <span className="min-w-0 flex-1 truncate text-fg-2">{a.title}</span>
                    <Badge tone={a.state === ApprovalState.APPROVED ? "ok" : "neutral"}>{ApprovalState[a.state].toLowerCase()}</Badge>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}
