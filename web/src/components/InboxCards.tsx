import { useState } from "react";
import { Link } from "react-router";
import {
  AlertTriangle,
  BellRing,
  Check,
  CheckCircle2,
  Clock,
  Fingerprint,
  Lightbulb,
  MessageCircleQuestion,
  Newspaper,
  ScanSearch,
  Send,
  ShieldCheck,
  Smartphone,
  X,
} from "lucide-react";
import { ApprovalState, type Approval } from "../gen/nexus/v1/approval_pb";
import { TrustLevel } from "../gen/nexus/v1/common_pb";
import { NoticeKind } from "../gen/nexus/v1/event_pb";
import { actionClassLabel, cx, formatDuration, noticeKindLabel, timeAgo, tsMs, type Tone } from "../lib/format";
import { useDecide, useSendMessage } from "../lib/queries";
import { useEvents, type NoticeItem, type QuestionItem } from "../lib/store";
import { useNow } from "../lib/useNow";
import { useTask } from "../lib/selectors";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { buttonClass } from "./ui/buttonStyles";
import { Input } from "./ui/Input";
import { AgentIcon } from "./AgentIcon";
import { ArtifactView } from "./Artifact";
import { agentName, useAgentMap } from "./agentMeta";
import { RawDetails, ToolDetail } from "./ToolView";
import { toolTitle } from "../lib/tools";

function Shell({
  tone,
  icon,
  kind,
  title,
  meta,
  children,
  actions,
  selectable,
  selected,
  onSelect,
  className,
  testId,
  urgent,
}: {
  tone: Tone;
  icon: React.ReactNode;
  kind: string;
  title: React.ReactNode;
  meta?: React.ReactNode;
  children?: React.ReactNode;
  actions?: React.ReactNode;
  selectable?: boolean;
  selected?: boolean;
  onSelect?: (v: boolean) => void;
  className?: string;
  testId?: string;
  urgent?: boolean;
}) {
  // Only things waiting on you get a tinted border; everything else stays quiet.
  const bar: Record<Tone, string> = {
    warn: "border-warn/40",
    danger: "border-danger/40",
    info: "border-info/35",
    ok: "border-border",
    accent: "border-border",
    neutral: "border-border",
  };
  return (
    <div
      data-testid={testId}
      className={cx(
        "relative overflow-hidden rounded-xl border bg-surface shadow-card transition-colors duration-150",
        urgent ? bar[tone] : "border-border",
        selected && "ring-2 ring-accent/50",
        className,
      )}
    >
      <div className="flex gap-3 p-4">
        {selectable && (
          <input
            type="checkbox"
            checked={!!selected}
            onChange={(e) => onSelect?.(e.target.checked)}
            className="mt-1 size-4 shrink-0 accent-[var(--accent)]"
            aria-label="Select"
          />
        )}
        <div className="min-w-0 flex-1">
          <div className="mb-1 flex flex-wrap items-center gap-2 text-xs text-muted">
            <Badge tone={tone}>
              {icon}
              {kind}
            </Badge>
            {meta}
          </div>
          <div className="text-[14px] font-semibold text-fg">{title}</div>
          {children}
          {actions && <div className="mt-3 flex flex-wrap items-center gap-2">{actions}</div>}
        </div>
      </div>
    </div>
  );
}

function TaskLink({ taskId }: { taskId: string }) {
  const task = useTask(taskId);
  const agents = useAgentMap();
  if (!taskId) return null;
  return (
    <Link to={`/tasks/${taskId}`} className="inline-flex items-center gap-1 hover:text-fg">
      {task && <AgentIcon agentId={task.agentId} size="sm" className="!size-4 !rounded [&_svg]:!size-2.5" />}
      {task ? `${agentName(agents, task.agentId, task.workflowId)} · ${task.title}` : "task"}
    </Link>
  );
}

export function ApprovalCard({
  approval,
  selectable,
  selected,
  onSelect,
  compact,
}: {
  approval: Approval;
  selectable?: boolean;
  selected?: boolean;
  onSelect?: (v: boolean) => void;
  compact?: boolean;
}) {
  const decide = useDecide();
  const now = useNow(1000);
  const [showPayload, setShowPayload] = useState(false);
  const biometric = approval.level === TrustLevel.BIOMETRIC;
  const pending = approval.state === ApprovalState.PENDING;
  const exp = tsMs(approval.expiresAt);
  const left = exp ? exp - now : 0;
  const decided = !pending;
  return (
    <Shell
      tone={decided ? (approval.state === ApprovalState.APPROVED ? "ok" : "neutral") : biometric ? "danger" : "warn"}
      icon={biometric ? <Fingerprint className="size-3" /> : <ShieldCheck className="size-3" />}
      kind={biometric ? "Face ID approval" : "Approval"}
      testId="approval-card"
      urgent={pending}
      selectable={selectable && pending}
      selected={selected}
      onSelect={onSelect}
      meta={
        <>
          <Badge>{actionClassLabel[approval.actionClass]}</Badge>
          {pending && exp > 0 && (
            <span className={cx("inline-flex items-center gap-1 tabular", left < 10 * 60_000 && "text-danger")}>
              <Clock className="size-3" />
              expires in {formatDuration(left)}
            </span>
          )}
          {approval.nodeId && <span>on {approval.nodeId}</span>}
          <span>· {timeAgo(tsMs(approval.createdAt), now)}</span>
        </>
      }
      title={approvalTitle(approval)}
      actions={
        pending ? (
          <>
            {biometric ? (
              <Button
                variant="outline"
                size="sm"
                icon={<Smartphone className="size-3.5" />}
                title="Biometric approvals are signed by the Secure Enclave on your iPhone. The web dashboard cannot sign them yet."
                onClick={() => setShowPayload(true)}
              >
                Approve on iPhone
              </Button>
            ) : (
              <Button
                variant="success"
                size="sm"
                icon={<Check className="size-3.5" />}
                loading={decide.isPending && decide.variables?.approve === true}
                onClick={() => decide.mutate({ approval, approve: true })}
              >
                Approve
              </Button>
            )}
            <Button
              variant="secondary"
              size="sm"
              icon={<X className="size-3.5" />}
              loading={decide.isPending && decide.variables?.approve === false}
              onClick={() => decide.mutate({ approval, approve: false })}
            >
              Deny
            </Button>
            {!compact && approval.previews.length > 1 && (
              <Button variant="ghost" size="sm" onClick={() => setShowPayload((v) => !v)}>
                {showPayload ? "Fewer previews" : "All previews"}
              </Button>
            )}
          </>
        ) : (
          <Badge tone={approval.state === ApprovalState.APPROVED ? "ok" : "neutral"}>
            {approval.state === ApprovalState.APPROVED ? <CheckCircle2 className="size-3" /> : <X className="size-3" />}
            {ApprovalState[approval.state].toLowerCase()}
          </Badge>
        )
      }
    >
      <ApprovalBody approval={approval} />
      {!compact && approval.taskId && (
        <div className="mt-2 text-xs text-muted">
          <TaskLink taskId={approval.taskId} />
        </div>
      )}
      {biometric && pending && showPayload && (
        <div className="mt-3 flex items-start gap-2 rounded-lg bg-surface-2 p-3 text-xs text-fg-2">
          <Fingerprint className="mt-0.5 size-4 shrink-0 text-accent" />
          <div>
            This action needs a <b>Face ID signature</b> from your paired iPhone — check the notification or Live Activity there.
            The node verifies the signature itself, so the web dashboard (and the Hub) cannot approve it.
          </div>
        </div>
      )}
      {approval.payload && <RawDetails value={approval.payload} />}
      {!compact && approval.previews.length > 0 && (
        <div className="mt-3 grid gap-2">
          {approval.previews.slice(0, showPayload ? undefined : 1).map((p) => (
            <ArtifactView key={p.id || p.name} att={p} />
          ))}
        </div>
      )}
    </Shell>
  );
}

export function QuestionCard({ item, compact }: { item: QuestionItem; compact?: boolean }) {
  const now = useNow(30_000);
  const q = item.question;
  return (
    <Shell
      tone="info"
      icon={<MessageCircleQuestion className="size-3" />}
      kind="Question"
      urgent={!item.answered}
      testId="question-card"
      meta={
        <>
          {q.header && <span className="rounded-md bg-info-soft px-1.5 py-0.5 text-[11px] font-medium text-info">{q.header}</span>}
          <span>{timeAgo(item.at, now)}</span>
        </>
      }
      title={q.text}
    >
      <QuestionOptions item={item} />
      {!compact && item.taskId && (
        <div className="mt-2 text-xs text-muted">
          <TaskLink taskId={item.taskId} />
        </div>
      )}
    </Shell>
  );
}

/**
 * The answer UI of a question: option cards (label + description), multi-select with a submit
 * button, and an "Other…" free-text answer. Used by the Inbox card and inline in the chat bubble.
 */
export function QuestionOptions({ item }: { item: QuestionItem }) {
  const send = useSendMessage();
  const [text, setText] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [other, setOther] = useState(false);
  const [answer, setAnswer] = useState<string | null>(null);
  const q = item.question;
  const options = q.options.length ? q.options.map((o) => ({ label: o.label, description: o.description })) : q.choices.map((c) => ({ label: c, description: "" }));
  const submit = (choice: string, free: string) => {
    setAnswer(free || choice);
    send.mutate({ taskId: item.taskId, questionId: q.questionId, choice, text: free }, { onError: () => setAnswer(null) });
  };
  if (item.answered || answer) {
    return (
      <div className="mt-2 flex items-center gap-2 text-[13px] text-fg-2">
        <Badge tone="ok">
          <Check className="size-3" />
          answered
        </Badge>
        {answer && <span className="min-w-0 truncate">{answer}</span>}
      </div>
    );
  }
  return (
    <div className="mt-2.5">
      <div className="grid gap-1.5" role={q.multiSelect ? "group" : undefined} aria-label={q.text}>
        {options.map((o) => {
          const on = picked.includes(o.label);
          return (
            <button
              key={o.label}
              type="button"
              aria-pressed={q.multiSelect ? on : undefined}
              disabled={send.isPending}
              onClick={() => (q.multiSelect ? setPicked((p) => (on ? p.filter((x) => x !== o.label) : [...p, o.label])) : submit(o.label, ""))}
              className={cx(
                "flex min-h-11 w-full items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:outline-none",
                on ? "border-accent bg-accent-soft" : "border-border bg-surface hover:border-border-strong hover:bg-surface-2",
              )}
            >
              {q.multiSelect && (
                <span className={cx("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded border", on ? "border-accent bg-accent text-accent-fg" : "border-border-strong")} aria-hidden>
                  {on && <Check className="size-3" />}
                </span>
              )}
              <span className="min-w-0">
                <span className="block text-[13.5px] font-medium text-fg">{o.label}</span>
                {o.description && <span className="mt-0.5 block text-[12.5px] text-fg-2">{o.description}</span>}
              </span>
            </button>
          );
        })}
        {q.allowFreeText &&
          (other ? (
            <form
              className="flex items-center gap-1.5"
              onSubmit={(e) => {
                e.preventDefault();
                const t = text.trim();
                if (t) submit(q.multiSelect ? picked.join(", ") : "", t);
              }}
            >
              <Input autoFocus value={text} onChange={(e) => setText(e.target.value)} placeholder="Type your answer…" aria-label="Your answer" className="h-9 pointer-coarse:h-11" />
              <Button type="submit" size="icon" variant="primary" aria-label="Send answer" disabled={!text.trim()} loading={send.isPending}>
                <Send className="size-3.5" />
              </Button>
            </form>
          ) : (
            <button
              type="button"
              onClick={() => setOther(true)}
              className="flex min-h-11 w-full items-center rounded-lg border border-dashed border-border px-3 py-2 text-left text-[13px] text-fg-2 hover:border-border-strong hover:text-fg focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:outline-none"
            >
              Other…
            </button>
          ))}
      </div>
      {q.multiSelect && (
        <Button className="mt-2" size="sm" variant="primary" disabled={!picked.length} loading={send.isPending} onClick={() => submit(picked.join(", "), "")}>
          Submit {picked.length ? `(${picked.length})` : ""}
        </Button>
      )}
    </div>
  );
}

/** Older approvals say "X wants to use Bash"; give them the readable form. */
function approvalTitle(a: Approval): string {
  const m = /^(.*) wants to use (\S+)$/.exec(a.title);
  const p = a.payload as { tool?: unknown; input?: unknown } | undefined;
  if (!m || typeof p?.tool !== "string") return a.title;
  const t = toolTitle(p.tool, (p.input ?? {}) as Record<string, unknown>);
  if (t.title.startsWith("Used ")) return a.title;
  return `${m[1]} wants to: ${t.title.charAt(0).toLowerCase()}${t.title.slice(1)}`;
}

function ApprovalBody({ approval }: { approval: Approval }) {
  const p = approval.payload as { tool?: unknown; input?: unknown; cwd?: unknown } | undefined;
  if (p && typeof p.tool === "string") {
    return (
      <div className="mt-2">
        <ToolDetail tool={p.tool} input={(p.input ?? {}) as Record<string, unknown>} cwd={typeof p.cwd === "string" ? p.cwd : undefined} />
      </div>
    );
  }
  if (p && Object.keys(p).length) {
    return (
      <div className="mt-2">
        {approval.summary && <p className="mb-2 text-[13px] text-fg-2">{approval.summary}</p>}
        <ToolDetail tool={approval.title} input={p as Record<string, unknown>} />
      </div>
    );
  }
  return approval.summary ? <p className="mt-1 text-[13px] text-fg-2">{approval.summary}</p> : null;
}

const noticeIcon: Record<NoticeKind, React.ReactNode> = {
  [NoticeKind.UNSPECIFIED]: <BellRing className="size-3" />,
  [NoticeKind.RESULT]: <CheckCircle2 className="size-3" />,
  [NoticeKind.ALERT]: <AlertTriangle className="size-3" />,
  [NoticeKind.SUGGESTION]: <Lightbulb className="size-3" />,
  [NoticeKind.FACT_REVIEW]: <ScanSearch className="size-3" />,
  [NoticeKind.BRIEF]: <Newspaper className="size-3" />,
};

const noticeTone: Record<NoticeKind, Tone> = {
  [NoticeKind.UNSPECIFIED]: "neutral",
  [NoticeKind.RESULT]: "ok",
  [NoticeKind.ALERT]: "danger",
  [NoticeKind.SUGGESTION]: "accent",
  [NoticeKind.FACT_REVIEW]: "accent",
  [NoticeKind.BRIEF]: "info",
};

export function NoticeCard({
  item,
  selectable,
  selected,
  onSelect,
  compact,
}: {
  item: NoticeItem;
  selectable?: boolean;
  selected?: boolean;
  onSelect?: (v: boolean) => void;
  compact?: boolean;
}) {
  const dismiss = useEvents((s) => s.dismiss);
  const now = useNow(30_000);
  const n = item.notice;
  return (
    <Shell
      tone={noticeTone[n.kind]}
      icon={noticeIcon[n.kind]}
      kind={noticeKindLabel[n.kind]}
      meta={<span>{timeAgo(item.at, now)}</span>}
      title={n.title}
      selectable={selectable}
      selected={selected}
      onSelect={onSelect}
      actions={
        <>
          {n.kind === NoticeKind.FACT_REVIEW && (
            <>
              <Button size="sm" variant="success" icon={<Check className="size-3.5" />} onClick={() => dismiss([n.id])}>
                Confirm
              </Button>
              <Button size="sm" icon={<X className="size-3.5" />} onClick={() => dismiss([n.id])}>
                Reject
              </Button>
            </>
          )}
          {n.kind === NoticeKind.SUGGESTION && !n.data?.draft_id && (
            <Button size="sm" variant="primary" onClick={() => dismiss([n.id])}>
              Apply
            </Button>
          )}
          {typeof n.data?.draft_id === "string" && n.data.draft_id && (
            <Link to={`/architect/${n.data.draft_id}`} className={buttonClass("primary")}>
              Open draft
            </Link>
          )}
          {item.taskId && !compact && (
            <Link to={`/tasks/${item.taskId}`}>
              <Button size="sm" variant="ghost">
                Open task
              </Button>
            </Link>
          )}
          <Button size="sm" variant="ghost" onClick={() => dismiss([n.id])}>
            Dismiss
          </Button>
        </>
      }
    >
      {n.body && <p className="mt-1 text-[13px] text-fg-2">{n.body}</p>}
      {!compact && n.notes.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {n.notes.map((r) => (
            <Link key={r.id || r.path} to={`/vault?note=${encodeURIComponent(r.id || r.path)}`} className="rounded-md bg-accent-soft px-1.5 py-0.5 text-xs text-accent hover:underline">
              {r.title || r.path}
            </Link>
          ))}
        </div>
      )}
    </Shell>
  );
}
