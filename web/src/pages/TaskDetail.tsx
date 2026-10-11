import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ArrowLeft, ArrowUpCircle, ChevronRight, Copy, OctagonX, RotateCcw, Smartphone } from "lucide-react";
import { useCancelTask, useEscalateTask, useRetryTask, useTaskQuery } from "../lib/queries";
import { useTask, useTimeline, useRunSummary } from "../lib/selectors";
import { useEvents } from "../lib/store";
import { api } from "../lib/api";
import { toast } from "../lib/toast";
import { RunView } from "../components/RunView";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Tabs } from "../components/ui/Tabs";
import { Badge } from "../components/ui/Badge";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge, StateIcon } from "../components/StateBadge";
import { agentName, useAgentMap } from "../components/agentMeta";
import { costMinor, cx, formatClock, formatDateTime, formatMoney, formatTokens, isActive, shortId, taskStateLabel, tierLabel, tsDate, tsMs } from "../lib/format";
import type { Event } from "../gen/nexus/v1/event_pb";
import type { Task } from "../gen/nexus/v1/task_pb";
import { toJson } from "@bufbuild/protobuf";
import { EventSchema } from "../gen/nexus/v1/event_pb";

function eventSummary(e: Event): { kind: string; text: string; tone?: string } {
  const b = e.body;
  switch (b.case) {
    case "taskUpdated":
      return { kind: "state", text: b.value.task ? taskStateLabel[b.value.task.state] + (b.value.task.nodeId ? ` · ${b.value.task.nodeId}` : "") : "" };
    case "runOutput":
      return { kind: "output", text: b.value.text.replace(/\s+/g, " ").trim() || "(whitespace)" };
    case "toolCall":
      return { kind: "tool", text: `${b.value.tool} ${JSON.stringify(b.value.input ?? {})}`, tone: "text-info" };
    case "toolResult":
      return { kind: "result", text: b.value.error || `${b.value.durationMs}ms ${JSON.stringify(b.value.output ?? {})}`, tone: b.value.error ? "text-danger" : undefined };
    case "question":
      return { kind: "question", text: b.value.text, tone: "text-warn" };
    case "approval":
      return { kind: "approval", text: `${b.value.title} — ${["", "pending", "approved", "denied", "expired"][b.value.state]}`, tone: "text-warn" };
    case "cost":
      return { kind: "cost", text: `${formatMoney(Number(b.value.cost?.amount?.minorUnits ?? 0n))} · ${formatTokens(b.value.cost?.inputTokens ?? 0n)} in / ${formatTokens(b.value.cost?.outputTokens ?? 0n)} out · ${b.value.cost?.model}` };
    case "artifact":
      return { kind: "artifact", text: b.value.attachment?.name ?? "" };
    case "notice":
      return { kind: "notice", text: b.value.title };
    case "userMessage":
      return { kind: "you", text: b.value.text };
    case "nodeStatus":
      return { kind: "node", text: b.value.nodeId };
    default:
      return { kind: "event", text: "" };
  }
}

function EventTimeline({ events }: { events: Event[] }) {
  const [open, setOpen] = useState<bigint | null>(null);
  const [hideOutput, setHideOutput] = useState(false);
  const list = hideOutput ? events.filter((e) => e.body.case !== "runOutput") : events;
  return (
    <div>
      <div className="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted">
        <span>{events.length} events</span>
        <span className="flex-1" />
        <label className="flex items-center gap-1.5">
          <input type="checkbox" checked={hideOutput} onChange={(e) => setHideOutput(e.target.checked)} className="accent-[var(--accent)]" />
          Hide output chunks
        </label>
      </div>
      <div className="max-h-[70vh] overflow-y-auto font-mono text-[11.5px] scrollbar-thin">
        {list.map((e) => {
          const s = eventSummary(e);
          const isOpen = open === e.seq;
          return (
            <div key={e.seq.toString()} className="border-b border-border/60 last:border-0">
              <button onClick={() => setOpen(isOpen ? null : e.seq)} className="flex w-full items-center gap-3 px-4 py-1.5 text-left hover:bg-surface-2">
                <ChevronRight className={cx("size-3 shrink-0 text-muted transition-transform", isOpen && "rotate-90")} />
                <span className="w-14 shrink-0 text-muted tabular">#{e.seq.toString()}</span>
                <span className="hidden w-16 shrink-0 text-muted tabular sm:block">{formatClock(tsDate(e.at))}</span>
                <span className="w-16 shrink-0 font-semibold text-fg-2">{s.kind}</span>
                <span className={cx("min-w-0 flex-1 truncate text-fg-2", s.tone)}>{s.text}</span>
                <span className="hidden shrink-0 text-muted md:block">{e.actor}</span>
              </button>
              {isOpen && (
                <pre className="mx-4 mb-2 max-h-72 overflow-auto rounded-md bg-surface-2 p-2.5 text-fg-2 scrollbar-thin">
                  {JSON.stringify(toJson(EventSchema, e), null, 2)}
                </pre>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function TreeNode({ task, all, current, depth }: { task: Task; all: Task[]; current: string; depth: number }) {
  const kids = all.filter((t) => t.parentId === task.id).sort((a, b) => tsMs(a.createdAt) - tsMs(b.createdAt));
  const agents = useAgentMap();
  return (
    <div>
      <Link
        to={`/tasks/${task.id}`}
        className={cx("flex items-center gap-2 rounded-lg py-1.5 pr-2 text-[13px] hover:bg-surface-2", task.id === current && "bg-accent-soft")}
        style={{ paddingLeft: 8 + depth * 18 }}
      >
        {depth > 0 && <span className="text-border-strong">└</span>}
        <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} size="sm" />
        <span className="min-w-0 flex-1 truncate text-fg">{task.title}</span>
        <span className="hidden text-xs text-muted sm:inline">{agentName(agents, task.agentId, task.workflowId)}</span>
        <StateIcon state={task.state} className="size-3.5 text-muted" />
      </Link>
      {kids.map((k) => (
        <TreeNode key={k.id} task={k} all={all} current={current} depth={depth + 1} />
      ))}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 py-1.5 text-[13px]">
      <span className="text-muted">{label}</span>
      <span className="min-w-0 text-right break-words text-fg">{children}</span>
    </div>
  );
}

export default function TaskDetail() {
  const { id } = useParams();
  const q = useTaskQuery(id);
  const task = useTask(id) ?? q.data?.task;
  const tasks = useEvents((s) => s.tasks);
  const events = useTimeline(id);
  const summary = useRunSummary(id);
  const agents = useAgentMap();
  const navigate = useNavigate();
  const cancel = useCancelTask();
  const escalate = useEscalateTask();
  const retry = useRetryTask();
  const [tab, setTab] = useState<"run" | "events">("run");

  const all = useMemo(() => Object.values(tasks), [tasks]);
  const root = task ? (tasks[task.rootId] ?? (task.parentId ? tasks[task.parentId] : undefined) ?? task) : undefined;
  const hasTree = !!root && all.some((t) => t.rootId === root.id && t.id !== root.id);

  if (!task) {
    if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-80" />
        <Skeleton className="h-96" />
      </div>
    );
  }
  const active = isActive(task.state);
  const cost = summary.costMinor || costMinor(task.cost);

  return (
    <div>
      <button onClick={() => navigate(-1)} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ArrowLeft className="size-3.5" /> Back
      </button>
      <div className="mb-5 flex flex-wrap items-start gap-3">
        <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} size="lg" />
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight text-fg">{task.title || task.prompt}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted">
            <StateBadge state={task.state} />
            <span>{agentName(agents, task.agentId, task.workflowId)}</span>
            <span>·</span>
            <span>{formatDateTime(tsDate(task.createdAt))}</span>
            <span>·</span>
            <button
              className="inline-flex items-center gap-1 font-mono hover:text-fg"
              onClick={() => {
                void navigator.clipboard?.writeText(task.id);
                toast.info("Copied task id");
              }}
            >
              {shortId(task.id)} <Copy className="size-3" />
            </button>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button icon={<Smartphone className="size-3.5" />} onClick={() => api().tasks.followTask({ taskId: task.id }).then(() => toast.success("Following on iPhone", "A Live Activity will appear on your phone."), (e) => toast.error("Follow failed", e))}>
            Follow on iPhone
          </Button>
          <Button icon={<RotateCcw className="size-3.5" />} loading={retry.isPending} onClick={() => retry.mutate({ prompt: task.prompt, agentId: task.agentId })}>
            Retry
          </Button>
          <Button icon={<ArrowUpCircle className="size-3.5" />} loading={escalate.isPending} onClick={() => escalate.mutate({ id: task.id })}>
            Escalate
          </Button>
          {active && (
            <Button variant="danger" icon={<OctagonX className="size-3.5" />} loading={cancel.isPending} onClick={() => cancel.mutate(task.id)}>
              Cancel
            </Button>
          )}
        </div>
      </div>

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
        <Card className="min-w-0 overflow-hidden">
          <div className="border-b border-border px-3 py-2">
            <Tabs<"run" | "events">
              value={tab}
              onChange={setTab}
              items={[
                { id: "run", label: "Live run" },
                { id: "events", label: "Event timeline", count: events.length },
              ]}
            />
          </div>
          {tab === "run" ? <RunView task={task} showHeader={false} /> : <EventTimeline events={events} />}
        </Card>

        <div className="space-y-5">
          <Card className="px-4 py-2">
            <Field label="State">{taskStateLabel[task.state]}</Field>
            <Field label="Agent">
              {task.agentId ? (
                <Link to={`/agents/${task.agentId}`} className="hover:underline">
                  {agentName(agents, task.agentId)}
                </Link>
              ) : (
                "—"
              )}
            </Field>
            {task.workflowId && <Field label="Workflow">{task.workflowId}</Field>}
            <Field label="Machine">
              {task.nodeId ? (
                <Link to={`/machines/${task.nodeId}`} className="hover:underline">
                  {task.nodeId}
                </Link>
              ) : (
                "—"
              )}
            </Field>
            <Field label="Tier">{tierLabel[task.tier]}</Field>
            <Field label="Source">{task.source || "—"}</Field>
            <Field label="Cost">{formatMoney(cost, task.cost?.amount?.currency)}</Field>
            <Field label="Tokens">
              {formatTokens(summary.inputTokens || Number(task.cost?.inputTokens ?? 0n))} in · {formatTokens(summary.outputTokens || Number(task.cost?.outputTokens ?? 0n))} out
            </Field>
            {task.cost?.model && <Field label="Model">{task.cost.model}</Field>}
            <Field label="Updated">{formatDateTime(tsDate(task.updatedAt))}</Field>
            {task.episode && (
              <Field label="Episode">
                <Link to={`/vault?note=${encodeURIComponent(task.episode.id || task.episode.path)}`} className="text-accent hover:underline">
                  {task.episode.title || task.episode.path}
                </Link>
              </Field>
            )}
          </Card>

          {hasTree && root && (
            <Card>
              <CardHeader title="Task tree" subtitle="Multi-agent run" />
              <div className="p-2">
                <TreeNode task={root} all={all} current={task.id} depth={0} />
              </div>
            </Card>
          )}

          {task.summary && (
            <Card className="p-4">
              <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Result</div>
              <p className="text-[13px] text-fg-2">{task.summary}</p>
            </Card>
          )}

          <Card className="p-4">
            <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Prompt</div>
            <p className="text-[13px] whitespace-pre-wrap text-fg-2">{task.prompt || "—"}</p>
            {task.parentId && (
              <div className="mt-2">
                <Badge>child of {shortId(task.parentId)}</Badge>
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}
