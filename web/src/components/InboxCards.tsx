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
import { Input } from "./ui/Input";
import { AgentIcon } from "./AgentIcon";
import { ArtifactView } from "./Artifact";
import { agentName, useAgentMap } from "./agentMeta";

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
}) {
  const bar: Record<Tone, string> = {
    warn: "before:bg-warn",
    danger: "before:bg-danger",
    info: "before:bg-info",
    ok: "before:bg-ok",
    accent: "before:bg-accent",
    neutral: "before:bg-border-strong",
  };
  return (
    <div
      data-testid={testId}
      className={cx(
        "relative overflow-hidden rounded-xl border border-border bg-surface shadow-card before:absolute before:inset-y-0 before:left-0 before:w-[3px]",
        bar[tone],
        selected && "ring-2 ring-accent/50",
        className,
      )}
    >
      <div className="flex gap-3 p-4 pl-5">
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
      title={approval.title}
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
            {!compact && (
              <Button variant="ghost" size="sm" onClick={() => setShowPayload((v) => !v)}>
                {showPayload ? "Hide details" : "Details"}
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
      {approval.summary && <p className="mt-1 text-[13px] text-fg-2">{approval.summary}</p>}
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
      {showPayload && approval.payload && (
        <pre className="mt-3 max-h-56 overflow-auto rounded-lg border border-border bg-surface-2 p-3 font-mono text-[11.5px] text-fg-2 scrollbar-thin">
          {JSON.stringify(approval.payload, null, 2)}
        </pre>
      )}
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
  const send = useSendMessage();
  const [text, setText] = useState("");
  const now = useNow(30_000);
  const q = item.question;
  const answer = (choice?: string, free?: string) =>
    send.mutate({ taskId: item.taskId, questionId: q.questionId, choice: choice ?? "", text: free ?? "" });
  return (
    <Shell
      tone="info"
      icon={<MessageCircleQuestion className="size-3" />}
      kind="Question"
      meta={<span>{timeAgo(item.at, now)}</span>}
      title={q.text}
      actions={
        item.answered ? (
          <Badge tone="ok">
            <Check className="size-3" />
            answered
          </Badge>
        ) : (
          <>
            {q.choices.map((c) => (
              <Button key={c} size="sm" variant="secondary" loading={send.isPending && send.variables?.choice === c} onClick={() => answer(c)}>
                {c}
              </Button>
            ))}
            {q.allowFreeText && (
              <form
                className="flex min-w-[220px] flex-1 items-center gap-1.5"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (text.trim()) answer(undefined, text.trim());
                  setText("");
                }}
              >
                <Input value={text} onChange={(e) => setText(e.target.value)} placeholder="Or type a reply…" className="h-8" />
                <Button type="submit" size="icon" variant="primary" aria-label="Send reply" disabled={!text.trim()}>
                  <Send className="size-3.5" />
                </Button>
              </form>
            )}
          </>
        )
      }
    >
      {!compact && item.taskId && (
        <div className="mt-1 text-xs text-muted">
          <TaskLink taskId={item.taskId} />
        </div>
      )}
    </Shell>
  );
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
          {n.kind === NoticeKind.SUGGESTION && (
            <Button size="sm" variant="primary" onClick={() => dismiss([n.id])}>
              Apply
            </Button>
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
