import { useMemo, useRef, useState } from "react";
import { VirtualList } from "../components/VirtualList";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, ArrowUpCircle, CheckCircle2, ChevronRight, Copy, Cpu, Loader2, RotateCcw, Smartphone, Timer } from "lucide-react";
import { useEscalateTask, useRetryTask, useTaskQuery } from "../lib/queries";
import { useChildren, useTask, useTimeline, useRunSummary } from "../lib/selectors";
import { useTaskHistory } from "../lib/events";
import { useNow } from "../lib/useNow";
import { buildItems } from "../lib/runItems";
import { BACKGROUND_WARNING, engineLabel, engineModelLabel, modelLabel, untrackedBackgroundWarning, useRunModel } from "../lib/runMeta";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { throughSeq, useEvents } from "../lib/store";
import { api } from "../lib/api";
import { toast } from "../lib/toast";
import { InlineApproval, InlineQuestion, RunView, ToolCallRow } from "../components/RunView";
import { StreamingMarkdown } from "../components/StreamingMarkdown";
import { ArtifactView } from "../components/Artifact";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Tabs } from "../components/ui/Tabs";
import { Badge } from "../components/ui/Badge";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge, StateIcon } from "../components/StateBadge";
import { agentName, useAgentMap } from "../components/agentMeta";
import {
  costMinor,
  cx,
  formatClock,
  formatDateTime,
  formatDuration,
  formatMoney,
  formatTokens,
  isActive,
  shortId,
  taskStateLabel,
  tierLabel,
  tsDate,
  tsMs,
} from "../lib/format";
import type { Event } from "../gen/nexus/v1/event_pb";
import type { Task } from "../gen/nexus/v1/task_pb";
import { toJson } from "@bufbuild/protobuf";
import { EventSchema } from "../gen/nexus/v1/event_pb";

function eventSummary(e: Event): { kind: string; text: string; tone?: string } {
  const b = e.body;
  switch (b.case) {
    case "taskUpdated":
      return { kind: "state", text: b.value.task ? taskStateLabel[b.value.task.state] + (b.value.task.nodeId ? ` · ${b.value.task.nodeId}` : "") : "" };
    case "runOutput": {
      const through = throughSeq(e);
      const span = through > e.seq ? ` (${(through - e.seq + 1n).toString()} chunks)` : "";
      return { kind: "output", text: (b.value.text.replace(/\s+/g, " ").trim().slice(0, 300) || "(whitespace)") + span };
    }
    case "toolCall":
      return { kind: "tool", text: `${b.value.tool} ${JSON.stringify(b.value.input ?? {})}`, tone: "text-info" };
    case "toolResult":
      return {
        kind: "result",
        text: b.value.error || `${b.value.durationMs}ms ${JSON.stringify(b.value.output ?? {})}`,
        tone: b.value.error ? "text-danger" : undefined,
      };
    case "question":
      return { kind: "question", text: b.value.text, tone: "text-warn" };
    case "approval":
      return { kind: "approval", text: `${b.value.title} — ${["", "pending", "approved", "denied", "expired"][b.value.state]}`, tone: "text-warn" };
    case "cost":
      return {
        kind: "cost",
        text: `${formatMoney(Number(b.value.cost?.amount?.minorUnits ?? 0n))} · ${formatTokens(b.value.cost?.inputTokens ?? 0n)} in / ${formatTokens(b.value.cost?.outputTokens ?? 0n)} out · ${b.value.cost?.model}`,
      };
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
  const scrollRef = useRef<HTMLDivElement>(null);
  const list = useMemo(() => (hideOutput ? events.filter((e) => e.body.case !== "runOutput") : events), [events, hideOutput]);
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
      <div ref={scrollRef} className="max-h-[70vh] overflow-y-auto font-mono text-[12px] scrollbar-thin">
        <VirtualList
          items={list}
          scrollRef={scrollRef}
          estimate={31}
          threshold={150}
          getKey={(e) => e.seq.toString()}
          render={(e) => {
            const s = eventSummary(e);
            const isOpen = open === e.seq;
            return (
              <div className="border-b border-border/60">
                <button
                  onClick={() => setOpen(isOpen ? null : e.seq)}
                  aria-expanded={isOpen}
                  className="flex min-h-[30px] w-full items-center gap-3 px-4 py-1.5 text-left hover:bg-surface-2"
                >
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
          }}
        />
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
        <span className="min-w-0 flex-1 truncate text-fg" title={`${task.title} — ${agentName(agents, task.agentId, task.workflowId)}`}>
          {task.title}
        </span>
        <StateIcon state={task.state} className="size-3.5 text-muted" />
      </Link>
      {kids.map((k) => (
        <TreeNode key={k.id} task={k} all={all} current={current} depth={depth + 1} />
      ))}
    </div>
  );
}

/** Result first, then readable steps, then sub-tasks: the task page's overview. */
function Overview({ task, onReply }: { task: Task; onReply: () => void }) {
  useTaskHistory(task.id);
  const events = useTimeline(task.id);
  const items = useMemo(() => buildItems(events, task.id), [events, task.id]);
  const children = useChildren(task.id);
  const active = isActive(task.state);
  const lastText = [...items].reverse().find((it) => it.k === "text" && it.text.trim());
  const answer = lastText && lastText.k === "text" ? lastText.text : "";
  const steps = items.filter((it) => it !== lastText && it.k !== "state");
  const running = children.filter((c) => isActive(c.state));
  const warn = untrackedBackgroundWarning(answer || task.summary, active, running.length);
  const failed = task.state === TaskState.FAILED;
  return (
    <div className="space-y-5 p-4 sm:p-5">
      <section aria-labelledby="result-h">
        <h2 id="result-h" className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">
          {active ? "Answer so far" : failed ? "What went wrong" : "Result"}
        </h2>
        {failed ? (
          <div role="alert" className="rounded-xl border border-danger/30 bg-danger-soft px-3.5 py-2.5 text-[13.5px] whitespace-pre-wrap text-danger">
            {task.summary || "The run failed."}
          </div>
        ) : answer || task.summary ? (
          <div className="rounded-xl border border-border bg-surface px-4 py-3 text-[14px]" data-testid="task-result">
            <StreamingMarkdown text={answer || task.summary} streaming={active} />
          </div>
        ) : (
          <div className="flex items-center gap-2 text-[13px] text-muted">
            {active ? (
              <>
                <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden /> Working…
              </>
            ) : (
              "No answer recorded."
            )}
          </div>
        )}
        {warn && (
          <div role="status" className="mt-2 flex items-start gap-2 rounded-xl border border-warn/40 bg-warn-soft px-3 py-2 text-[12.5px] text-fg" data-testid="background-warning">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden />
            {BACKGROUND_WARNING}
          </div>
        )}
        {active && (
          <Button className="mt-2" size="sm" variant="ghost" onClick={onReply}>
            Reply or steer in the live run
          </Button>
        )}
      </section>

      <section aria-labelledby="children-h">
        <h2 id="children-h" className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">
          Sub-tasks
        </h2>
        <div className={cx("mb-2 flex items-center gap-2 text-[13px]", running.length ? "text-info" : "text-fg-2")} data-testid="children-status">
          {running.length ? (
            <>
              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
              {running.length} sub-task{running.length === 1 ? "" : "s"} still running
            </>
          ) : active ? (
            <>
              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
              This task is still running{children.length ? "; its sub-tasks are done" : ""}
            </>
          ) : (
            <>
              <CheckCircle2 className="size-3.5 text-ok" aria-hidden />
              Nothing is still running for this task
            </>
          )}
        </div>
        {children.length > 0 && (
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
            {children.map((c) => (
              <li key={c.id}>
                <ChildRow task={c} />
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="steps-h">
        <h2 id="steps-h" className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">
          Steps <span className="font-normal normal-case tabular">({steps.length})</span>
        </h2>
        {steps.length === 0 ? (
          <div className="text-[13px] text-muted">{active ? "No steps yet." : "The agent answered directly, without tools."}</div>
        ) : (
          <ol className="space-y-2">
            {steps.map((it) => (
              <li key={it.key}>
                {it.k === "tool" ? (
                  <ToolCallRow call={it.call} result={it.result} streaming={active} />
                ) : it.k === "text" ? (
                  <div className="border-l-2 border-border pl-3 text-[13px] text-fg-2">
                    <StreamingMarkdown text={it.text} />
                  </div>
                ) : it.k === "question" ? (
                  <InlineQuestion qid={it.qid} ev={it.ev} />
                ) : it.k === "approval" ? (
                  <InlineApproval id={it.id} />
                ) : it.k === "artifact" ? (
                  <ArtifactView att={it.att} />
                ) : it.k === "user" ? (
                  <div className="text-[13px] text-fg-2">
                    <span className="font-medium text-fg">You: </span>
                    {it.text}
                  </div>
                ) : null}
              </li>
            ))}
          </ol>
        )}
      </section>
    </div>
  );
}

function ChildRow({ task }: { task: Task }) {
  const agents = useAgentMap();
  const model = useRunModel(task);
  return (
    <Link to={`/tasks/${task.id}`} className="flex min-h-11 items-center gap-2.5 px-3 py-2 hover:bg-surface-2 focus-visible:bg-surface-2 focus-visible:outline-none">
      <AgentIcon agentId={task.agentId} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium text-fg">{task.title || task.prompt}</div>
        <div className="truncate text-[11.5px] text-muted">{[agentName(agents, task.agentId, task.workflowId), engineModelLabel(model)].filter(Boolean).join(" · ")}</div>
      </div>
      <StateBadge state={task.state} />
    </Link>
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
  const escalate = useEscalateTask();
  const retry = useRetryTask();
  const [tab, setTab] = useState<"overview" | "run" | "events">("overview");
  const model = useRunModel(task);
  const now = useNow(1000);

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
  const agent = agents[task.agentId];
  const limit = agent?.limits?.maxCost;

  return (
    <div>
      <button
        onClick={() => (history.length > 1 ? navigate(-1) : navigate("/tasks"))}
        className="mb-3 inline-flex min-h-8 items-center gap-1 text-[13px] text-muted hover:text-fg"
      >
        <ArrowLeft className="size-3.5" aria-hidden /> Back
      </button>
      <div className="mb-5 flex flex-wrap items-start gap-3">
        <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} size="lg" />
        <div className="min-w-0 flex-1">
          <h1 className="text-[22px] leading-tight font-semibold tracking-[-0.01em] break-words text-fg">{task.title || task.prompt}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted">
            <StateBadge state={task.state} />
            <span className="font-medium text-fg-2">{agentName(agents, task.agentId, task.workflowId)}</span>
            {model.known && (
              <>
                <span>·</span>
                <span title={agent?.model ? `Agent model alias: ${agent.model}` : undefined} data-testid="task-model">
                  {engineModelLabel(model)}
                </span>
              </>
            )}
            {task.nodeId && (
              <>
                <span>·</span>
                <span className="inline-flex items-center gap-1">
                  <Cpu className="size-3" aria-hidden />
                  {task.nodeId}
                </span>
              </>
            )}
            <span>·</span>
            <span className="inline-flex items-center gap-1 tabular">
              <Timer className="size-3" aria-hidden />
              {formatDuration((active ? now : tsMs(task.updatedAt)) - tsMs(task.createdAt))}
            </span>
            <span>·</span>
            <span className="tabular">{formatMoney(cost, task.cost?.amount?.currency)}</span>
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
        <div className="flex gap-1.5">
          <Button
            variant="ghost"
            icon={<Smartphone className="size-4" />}
            aria-label="Follow on iPhone"
            title="Follow on iPhone (Live Activity)"
            onClick={() =>
              api()
                .tasks.followTask({ taskId: task.id })
                .then(
                  () => toast.success("Following on iPhone", "A Live Activity will appear on your phone."),
                  (e) => toast.error("Follow failed", e),
                )
            }
          >
            <span className="hidden sm:inline">Follow</span>
          </Button>
          <Button
            variant="ghost"
            icon={<RotateCcw className="size-4" />}
            aria-label="Run again"
            loading={retry.isPending}
            onClick={() => retry.mutate({ prompt: task.prompt, agentId: task.agentId }, { onSuccess: (r) => r.task && navigate(`/tasks/${r.task.id}`) })}
          >
            <span className="hidden sm:inline">Run again</span>
          </Button>
          {!active && (
            <Button variant="ghost" icon={<ArrowUpCircle className="size-4" />} aria-label="Try harder" title="Retry on the next tier" loading={escalate.isPending} onClick={() => escalate.mutate({ id: task.id })}>
              <span className="hidden sm:inline">Try harder</span>
            </Button>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
        <Card className="min-w-0 overflow-hidden">
          <div className="border-b border-border px-3 py-2">
            <Tabs<"overview" | "run" | "events">
              value={tab}
              onChange={setTab}
              items={[
                { id: "overview", label: "Overview" },
                { id: "run", label: active ? "Live run" : "Conversation" },
                { id: "events", label: "Event log", count: events.length },
              ]}
            />
          </div>
          {tab === "overview" ? (
            <Overview task={task} onReply={() => setTab("run")} />
          ) : tab === "run" ? (
            <RunView task={task} showHeader={false} />
          ) : (
            <EventTimeline events={events} />
          )}
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
            <Field label="Engine">{engineLabel(model.runtime) || "—"}</Field>
            <Field label="Model">
              <span className="font-mono text-[12px]" title={agent?.model ? `Agent model alias: ${agent.model}` : undefined}>
                {modelLabel(model) || "—"}
              </span>
            </Field>
            <Field label="Cost">
              {formatMoney(cost, task.cost?.amount?.currency)}
              {limit && Number(limit.minorUnits) > 0 ? <span className="text-muted"> of {formatMoney(Number(limit.minorUnits), limit.currency)}</span> : null}
            </Field>
            <Field label="Tokens">
              {formatTokens(summary.inputTokens || Number(task.cost?.inputTokens ?? 0n))} in ·{" "}
              {formatTokens(summary.outputTokens || Number(task.cost?.outputTokens ?? 0n))} out
            </Field>
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
