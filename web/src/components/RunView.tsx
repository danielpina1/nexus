import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import {
  ArrowDown,
  ArrowUpCircle,
  Check,
  ChevronRight,
  Coins,
  Cpu,
  ExternalLink,
  Loader2,
  OctagonX,
  Send,
  Timer,
  X,
} from "lucide-react";
import type { Event, ToolCall, ToolResult } from "../gen/nexus/v1/event_pb";
import { ActionClass } from "../gen/nexus/v1/common_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { actionClassLabel, cx, formatDuration, formatMoney, formatTokens, isActive, taskStateLabel, tsMs } from "../lib/format";
import { useCancelTask, useEscalateTask, usePendingMessages, useSendMessage, type PendingMessage } from "../lib/queries";
import { summarizeTimeline, useTimeline } from "../lib/selectors";
import { useEvents, type QuestionItem } from "../lib/store";
import { useTaskHistory } from "../lib/events";
import { useNow } from "../lib/useNow";
import { AgentIcon } from "./AgentIcon";
import { ArtifactView } from "./Artifact";
import { ApprovalCard, QuestionCard } from "./InboxCards";
import { StreamingMarkdown } from "./StreamingMarkdown";
import { buildItems, summaryShown } from "../lib/runItems";
import { QuestionsPreview, RawDetails, ToolDetail } from "./ToolView";
import { toolTitle } from "../lib/tools";
import { StateBadge, StateIcon } from "./StateBadge";
import { agentName, useAgentMap } from "./agentMeta";
import { Button } from "./ui/Button";
import { Input } from "./ui/Input";

/** The AskUserQuestion answers Claude Code got back ("…"="…" pairs in the tool result). */
function questionAnswers(result?: ToolResult): Record<string, string> | undefined {
  const text = (result?.output as { content?: unknown } | undefined)?.content;
  if (typeof text !== "string") return undefined;
  const out: Record<string, string> = {};
  for (const m of text.matchAll(/"([^"]+)"="([^"]*)"/g)) out[m[1]] = m[2];
  return Object.keys(out).length ? out : undefined;
}

export const ToolCallRow = memo(function ToolCallRow({ call, result, streaming, compact }: { call: ToolCall; result?: ToolResult; streaming: boolean; compact?: boolean }) {
  const [open, setOpen] = useState(false);
  const failed = !!result?.error;
  const risky = call.actionClass >= ActionClass.WRITE_EXTERNAL;
  const input = (call.input ?? {}) as Record<string, unknown>;
  const { title, icon: Icon } = toolTitle(call.tool, input);
  const isQuestion = call.tool === "AskUserQuestion";
  return (
    <div className={cx("rounded-lg border border-border", compact ? "bg-surface" : "bg-surface-2/60")} data-testid="tool-chip">
      <button
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex min-h-8 w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-xs focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:outline-none pointer-coarse:min-h-11"
      >
        <ChevronRight className={cx("size-3.5 shrink-0 text-muted transition-transform", open && "rotate-90")} aria-hidden />
        <Icon className="size-3.5 shrink-0 text-muted" aria-hidden />
        <span className="min-w-0 flex-1 truncate font-medium text-fg">{title}</span>
        {risky && <span className="hidden shrink-0 rounded bg-warn-soft px-1 text-[10px] text-warn sm:inline">{actionClassLabel[call.actionClass]}</span>}
        {result ? (
          <span className={cx("flex shrink-0 items-center gap-1 tabular", failed ? "text-danger" : "text-muted")}>
            {failed ? <X className="size-3" aria-label="failed" /> : <Check className="size-3 text-ok" aria-label="done" />}
            {Number(result.durationMs) >= 1000 ? `${(Number(result.durationMs) / 1000).toFixed(1)}s` : `${result.durationMs}ms`}
          </span>
        ) : streaming ? (
          <Loader2 className="size-3.5 shrink-0 animate-spin text-info" aria-label="running" />
        ) : null}
      </button>
      {open && (
        <div className="grid gap-2 border-t border-border p-2.5 text-[12px]">
          {isQuestion ? <QuestionsPreview input={input} answers={questionAnswers(result)} /> : <ToolDetail tool={call.tool} input={input} />}
          {result && failed && <div className="rounded-md bg-danger-soft px-2 py-1.5 text-[12px] whitespace-pre-wrap text-danger">{result.error}</div>}
          {result && !failed && !isQuestion && (
            <div>
              <div className="mb-1 text-[10px] font-semibold tracking-wide text-muted uppercase">Output</div>
              <pre className="max-h-48 overflow-auto rounded-md bg-surface p-2 font-mono text-[11.5px] whitespace-pre-wrap text-fg-2 scrollbar-thin">{formatOutput(result.output)}</pre>
            </div>
          )}
          <RawDetails value={{ tool: call.tool, input: call.input, output: result?.output, error: result?.error || undefined }} />
        </div>
      )}
    </div>
  );
});

function formatOutput(o: unknown): string {
  if (o && typeof o === "object" && "stdout" in o && typeof (o as { stdout: unknown }).stdout === "string") {
    return (o as { stdout: string }).stdout;
  }
  if (o && typeof o === "object" && Object.keys(o).length === 1) {
    const v = Object.values(o)[0];
    if (typeof v === "string") return v;
  }
  return JSON.stringify(o ?? {}, null, 2);
}

export function InlineQuestion({ qid, ev }: { qid: string; ev: Event }) {
  const stored = useEvents((s) => s.questions[qid]);
  if (ev.body.case !== "question") return null;
  const item: QuestionItem = stored ?? { seq: ev.seq, at: tsMs(ev.at), taskId: ev.taskId, actor: ev.actor, question: ev.body.value, answered: true };
  return <QuestionCard item={item} compact />;
}

export function InlineApproval({ id }: { id: string }) {
  const a = useEvents((s) => s.approvals[id]);
  if (!a) return null;
  return <ApprovalCard approval={a} compact />;
}

export function CostMeter({ minor, currency, limitMinor, tokensIn, tokensOut }: { minor: number; currency: string; limitMinor?: number; tokensIn: number; tokensOut: number }) {
  const pct = limitMinor ? Math.min(100, (minor / limitMinor) * 100) : 0;
  return (
    <div className="flex items-center gap-2 text-xs" title={`${formatTokens(tokensIn)} in · ${formatTokens(tokensOut)} out`}>
      <Coins className="size-3.5 text-muted" />
      <span className="font-medium text-fg tabular">{formatMoney(minor, currency)}</span>
      {limitMinor ? (
        <>
          <div className="h-1.5 w-16 overflow-hidden rounded-full bg-surface-3">
            <div className={cx("h-full rounded-full", pct > 85 ? "bg-danger" : pct > 60 ? "bg-warn" : "bg-ok")} style={{ width: `${Math.max(3, pct)}%` }} />
          </div>
          <span className="text-muted tabular">of {formatMoney(limitMinor, currency)}</span>
        </>
      ) : null}
    </div>
  );
}

/**
 * Keeps a scroller pinned to the bottom while content grows, unless the user scrolled up. Content
 * growth is observed directly (ResizeObserver), so late layout (images, markdown) still sticks.
 */
function useStickToBottom() {
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [away, setAway] = useState(false);
  useEffect(() => {
    const el = scroller.current;
    const inner = content.current;
    if (!el || !inner) return;
    el.scrollTop = el.scrollHeight;
    const ro = new ResizeObserver(() => {
      if (stick.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(inner);
    return () => ro.disconnect();
  }, []);
  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    stick.current = atBottom;
    setAway((a) => (a === !atBottom ? a : !atBottom));
  };
  const jump = () => {
    const el = scroller.current;
    if (!el) return;
    stick.current = true;
    setAway(false);
    el.scrollTo({ top: el.scrollHeight, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  };
  return { scroller, content, onScroll, away, jump };
}

const EMPTY_PENDING: PendingMessage[] = [];

export function RunView({ task, embedded, showHeader = true }: { task: Task; embedded?: boolean; showHeader?: boolean }) {
  useTaskHistory(task.id);
  const events = useTimeline(task.id);
  const agents = useAgentMap();
  const agent = agents[task.agentId];
  const items = useMemo(() => buildItems(events, task.id), [events, task.id]);
  const summary = useMemo(() => summarizeTimeline(events), [events]);
  const active = isActive(task.state);
  const now = useNow(1000);
  const { scroller, content, onScroll, away, jump } = useStickToBottom();
  const cancel = useCancelTask();
  const escalate = useEscalateTask();
  const send = useSendMessage();
  const pendingAll = usePendingMessages((s) => s.items);
  const pending = useMemo(() => {
    const p = pendingAll.filter((x) => x.taskId === task.id);
    if (!p.length) return EMPTY_PENDING;
    // Hide placeholders the stream already echoed back.
    const echoed = new Set(items.flatMap((it) => (it.k === "user" ? [it.text] : [])));
    return p.filter((x) => x.status === "failed" || !echoed.has(x.text));
  }, [pendingAll, items, task.id]);
  const [reply, setReply] = useState("");
  const [confirmCancel, setConfirmCancel] = useState(false);
  useEffect(() => {
    if (!confirmCancel) return;
    const t = setTimeout(() => setConfirmCancel(false), 4000);
    return () => clearTimeout(t);
  }, [confirmCancel]);

  const start = tsMs(task.createdAt);
  const end = active ? now : tsMs(task.updatedAt);
  const costMinor = summary.costMinor || Number(task.cost?.amount?.minorUnits ?? 0n);
  const limit = agent?.limits?.maxCost ? Number(agent.limits.maxCost.minorUnits) : undefined;

  return (
    <div className={cx("relative flex min-h-0 flex-col", embedded && "h-full")} data-testid="run-view">
      {showHeader && (
        <div className="flex items-center gap-3 border-b border-border px-4 py-3">
          <AgentIcon agentId={task.agentId} workflow={!!task.workflowId} />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <Link to={`/tasks/${task.id}`} className="truncate text-[14px] font-semibold text-fg hover:underline">
                {task.title || task.prompt}
              </Link>
              {embedded && (
                <Link to={`/tasks/${task.id}`} className="shrink-0 text-muted hover:text-fg" title="Open task" aria-label="Open task">
                  <ExternalLink className="size-3.5" />
                </Link>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-x-2 text-xs text-muted">
              <span>{agentName(agents, task.agentId, task.workflowId)}</span>
              {task.nodeId && (
                <span className="inline-flex items-center gap-1">
                  <Cpu className="size-3" aria-hidden /> {task.nodeId}
                </span>
              )}
              <span className="inline-flex items-center gap-1 tabular">
                <Timer className="size-3" aria-hidden /> {formatDuration(end - start)}
              </span>
            </div>
          </div>
          <StateBadge state={task.state} />
        </div>
      )}

      <div
        ref={scroller}
        onScroll={onScroll}
        className={cx("min-h-0 flex-1 overflow-y-auto overscroll-contain scrollbar-thin", !embedded && "max-h-[70vh] min-h-64")}
        aria-live="off"
      >
        <div ref={content} className="space-y-3 px-4 py-4">
          {task.prompt && (
            <div className="flex justify-end">
              <div className="max-w-[85%] rounded-2xl rounded-br-md bg-primary px-3.5 py-2 text-[13.5px] whitespace-pre-wrap text-primary-fg">{task.prompt}</div>
            </div>
          )}
          {items.length === 0 &&
            (active ? (
              <div className="flex items-center gap-2 text-[13px] text-muted">
                <Loader2 className="size-3.5 animate-spin" aria-hidden />
                {task.state === TaskState.QUEUED || task.state === TaskState.ROUTING ? "Finding the right machine…" : "Starting…"}
              </div>
            ) : (
              <div className="text-[13px] text-muted">{task.summary || "No output recorded."}</div>
            ))}
          {items.map((it, idx) => {
            const last = idx === items.length - 1;
            switch (it.k) {
              case "text":
                return <StreamingMarkdown key={it.key} text={it.text} streaming={last && active && it.streaming} className="text-[14px]" />;
              case "tool":
                return <ToolCallRow key={it.key} call={it.call} result={it.result} streaming={active} />;
              case "state":
                return (
                  <div key={it.key} className="flex items-center gap-2 text-[11.5px] text-muted">
                    <div className="h-px flex-1 bg-border" />
                    <StateIcon state={it.state} />
                    {taskStateLabel[it.state]}
                    <div className="h-px flex-1 bg-border" />
                  </div>
                );
              case "question":
                return <InlineQuestion key={it.key} qid={it.qid} ev={it.ev} />;
              case "approval":
                return <InlineApproval key={it.key} id={it.id} />;
              case "artifact":
                return <ArtifactView key={it.key} att={it.att} />;
              case "user":
                return (
                  <div key={it.key} className="flex justify-end">
                    <div className="max-w-[85%] rounded-2xl rounded-br-md bg-surface-3 px-3.5 py-2 text-[13.5px] whitespace-pre-wrap text-fg">{it.text}</div>
                  </div>
                );
            }
          })}
          {pending.map((p) => (
            <div key={p.id} className="flex flex-col items-end gap-1">
              <div className={cx("max-w-[85%] rounded-2xl rounded-br-md bg-surface-3 px-3.5 py-2 text-[13.5px] whitespace-pre-wrap text-fg", p.status === "sending" && "opacity-60")}>{p.text}</div>
              <span className={cx("text-[11px]", p.status === "failed" ? "text-danger" : "text-muted")}>{p.status === "failed" ? "Not sent" : "Sending…"}</span>
            </div>
          ))}
          {!active && task.summary && items.length > 0 && !summaryShown(items, task.summary) && (
            <div className="rounded-lg border border-border bg-surface-2 px-3 py-2 text-[13px] text-fg-2">
              <span className="font-medium text-fg">Result · </span>
              {task.summary}
            </div>
          )}
        </div>
      </div>

      {away && (
        <button
          onClick={jump}
          className="animate-fade-up absolute bottom-[132px] left-1/2 z-10 flex h-8 -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-surface px-3 text-xs font-medium text-fg shadow-pop hover:bg-surface-2"
        >
          <ArrowDown className="size-3.5" aria-hidden /> Latest
        </button>
      )}

      <div className="border-t border-border px-4 py-3">
        <div className="mb-2.5 flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <CostMeter minor={costMinor} currency={summary.currency} limitMinor={limit} tokensIn={summary.inputTokens} tokensOut={summary.outputTokens} />
          {summary.toolCalls > 0 && <span className="text-xs text-muted tabular">{summary.toolCalls} tool call{summary.toolCalls === 1 ? "" : "s"}</span>}
          <span className="flex-1" />
          {active && (
            <Button size="xs" variant="ghost" icon={<ArrowUpCircle className="size-3.5" />} loading={escalate.isPending} onClick={() => escalate.mutate({ id: task.id })} title="Retry on the next tier">
              Try harder
            </Button>
          )}
          {active &&
            (confirmCancel ? (
              <Button
                size="xs"
                variant="danger"
                icon={<OctagonX className="size-3.5" />}
                onClick={() => {
                  setConfirmCancel(false);
                  cancel.mutate(task.id);
                }}
              >
                Confirm cancel
              </Button>
            ) : (
              <Button size="xs" variant="outline" icon={<OctagonX className="size-3.5 text-danger" />} onClick={() => setConfirmCancel(true)}>
                Cancel
              </Button>
            ))}
        </div>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            const t = reply.trim();
            if (!t) return;
            send.mutate({ taskId: task.id, text: t });
            setReply("");
          }}
        >
          <Input value={reply} onChange={(e) => setReply(e.target.value)} placeholder={active ? "Reply to the agent…" : "Follow up…"} aria-label="Reply to the agent" className="pointer-coarse:h-11" />
          <Button type="submit" variant="primary" size="icon" aria-label="Send reply" disabled={!reply.trim()}>
            <Send className="size-3.5" />
          </Button>
        </form>
      </div>
    </div>
  );
}
