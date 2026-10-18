import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowLeft,
  Bot,
  CheckCircle2,
  CircleHelp,
  ExternalLink,
  FileText,
  FlaskConical,
  Lightbulb,
  Loader2,
  MessageSquarePlus,
  Rocket,
  ShieldAlert,
  ShieldBan,
  Trash2,
  Users,
  Wrench,
  Workflow,
  XCircle,
} from "lucide-react";
import {
  BUSY_STATES,
  draftKeys,
  draftKindLabel,
  draftStateLabel,
  draftStateTone,
  draftTitle,
  useDiscardDraft,
  useDraft,
  useDraftNotices,
  usePublishDraft,
  useRefineDraft,
  useTestDraft,
} from "../lib/architect";
import { useEvents } from "../lib/store";
import { splitFrontmatter } from "../lib/yaml";
import { cx, formatClock, timeAgo, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { Card, CardHeader } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { ConfirmButton } from "../components/ui/ConfirmButton";
import { Textarea } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { Markdown } from "../components/Markdown";
import { DiffView } from "../components/DiffView";
import { ApprovalCard } from "../components/InboxCards";
import { DraftKind, DraftState, type AgentDraft, type DraftNote, type DraftTestCase, type DraftTestResult } from "../gen/nexus/v1/architect_pb";
import { ApprovalState } from "../gen/nexus/v1/approval_pb";

/** Renders `code` spans in problem / fix text so tool names stand out. */
function RichLine({ text }: { text: string }) {
  const parts = text.split(/(`[^`]+`|\b(?:anthrex|architect|browser|email|fs|memory|node|notify|shell|team|vault|web)\.[\w.*]+)/g);
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <code key={i} className="rounded bg-surface-2 px-1 py-px font-mono text-[12px] text-fg">
            {p.replace(/^`|`$/g, "")}
          </code>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  );
}

const MODE: Record<string, { label: string; hint: string; tone: "ok" | "info" | "neutral" }> = {
  sandbox_task: { label: "sandbox", hint: "An ephemeral sandboxed task: read/compute tools only, everything else needs Face ID.", tone: "ok" },
  native_loop: { label: "native loop", hint: "In-process model loop: read/compute tools really ran; anything else was refused and listed.", tone: "info" },
  simulated: { label: "simulated", hint: "No sandbox or model available: the agent-architect role-played the draft. Treat results as indicative.", tone: "neutral" },
};

function noteIcon(kind: string) {
  return kind === "team" ? <Users className="size-3.5" aria-hidden /> : kind === "workflow" ? <Workflow className="size-3.5" aria-hidden /> : <Bot className="size-3.5" aria-hidden />;
}

function NoteView({ note }: { note: DraftNote }) {
  const [view, setView] = useState<"preview" | "markdown" | "diff">(note.diff ? "diff" : "preview");
  const { frontmatter, body } = useMemo(() => splitFrontmatter(note.markdown), [note.markdown]);
  const fm = Object.entries(frontmatter).filter(([k]) => !["type", "version"].includes(k));
  return (
    <div>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2">
        <span className="flex min-w-0 flex-1 items-center gap-1.5 font-mono text-[12.5px] text-fg-2">
          {noteIcon(note.kind)}
          <span className="truncate">{note.path}</span>
          {note.exists ? <Badge tone="warn">update</Badge> : <Badge tone="ok">new</Badge>}
        </span>
        <Tabs<"preview" | "markdown" | "diff">
          label={`View of ${note.path}`}
          value={view}
          onChange={setView}
          items={[{ id: "preview", label: "Preview" }, { id: "markdown", label: "Markdown" }, ...(note.diff ? [{ id: "diff" as const, label: "Diff" }] : [])]}
        />
      </div>
      <div className="p-4">
        {view === "preview" && (
          <div className="space-y-4">
            {fm.length > 0 && (
              <dl className="grid grid-cols-1 gap-x-4 gap-y-1.5 rounded-lg border border-border bg-surface-2/50 p-3 text-[12.5px] sm:grid-cols-[120px_minmax(0,1fr)]">
                {fm.map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt className="font-medium text-muted">{k}</dt>
                    <dd className="min-w-0 font-mono break-words text-fg">{typeof v === "string" ? v : JSON.stringify(v)}</dd>
                  </div>
                ))}
              </dl>
            )}
            <Markdown>{body || "_(no instructions)_"}</Markdown>
          </div>
        )}
        {view === "markdown" && (
          <pre className="max-h-[640px] overflow-auto rounded-lg border border-border bg-surface-2 p-3 font-mono text-[12px] leading-[19px] text-fg-2 scrollbar-thin">{note.markdown}</pre>
        )}
        {view === "diff" && <DiffView diff={note.diff} className="max-h-[640px]" />}
      </div>
    </div>
  );
}

function Notes({ notes }: { notes: DraftNote[] }) {
  const [i, setI] = useState(0);
  const cur = notes[Math.min(i, notes.length - 1)];
  return (
    <Card className="min-w-0 overflow-hidden">
      <CardHeader icon={<FileText className="size-4" />} title="Generated notes" subtitle={`${notes.length} ${notes.length === 1 ? "note" : "notes"} written to the vault on publish`} />
      {notes.length > 1 && (
        <div className="border-b border-border px-3 py-2">
          <Tabs<string>
            label="Notes"
            value={String(Math.min(i, notes.length - 1))}
            onChange={(v) => setI(Number(v))}
            items={notes.map((n, k) => ({ id: String(k), label: `${n.kind}: ${n.id}` }))}
          />
        </div>
      )}
      {cur ? <NoteView key={cur.path} note={cur} /> : <EmptyState compact icon={<FileText />} title="No notes yet" body="They appear when the draft is ready." />}
    </Card>
  );
}

function TestResult({ r }: { r: DraftTestResult }) {
  const m = MODE[r.mode] ?? { label: r.mode || "unknown", hint: "", tone: "neutral" as const };
  return (
    <div className="mt-2.5 space-y-2 rounded-lg border border-border bg-surface-2/40 p-3">
      <div className="flex flex-wrap items-center gap-1.5 text-xs">
        {r.passed ? (
          <Badge tone="ok">
            <CheckCircle2 className="size-3" aria-hidden /> passed
          </Badge>
        ) : (
          <Badge tone="danger">
            <XCircle className="size-3" aria-hidden /> failed
          </Badge>
        )}
        <Badge tone={m.tone} title={m.hint}>
          mode: {m.label}
        </Badge>
        {r.taskId && (
          <Link to={`/tasks/${r.taskId}`} className="inline-flex min-h-6 items-center gap-1 text-accent hover:underline">
            sandbox task <ExternalLink className="size-3" aria-hidden />
          </Link>
        )}
      </div>
      {m.hint && <p className="text-[11.5px] text-muted">{m.hint}</p>}
      {r.error && (
        <p role="alert" className="text-[12.5px] text-danger">
          {r.error}
        </p>
      )}
      {r.checks.length > 0 && (
        <ul className="space-y-0.5 font-mono text-[12px]" aria-label="Checks">
          {r.checks.map((c, k) => (
            <li key={k} className={cx(c.startsWith("✓") ? "text-ok" : c.startsWith("✗") ? "text-danger" : "text-muted")}>
              {c}
            </li>
          ))}
        </ul>
      )}
      {r.blockedActions.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="inline-flex items-center gap-1 text-xs font-medium text-warn">
            <ShieldBan className="size-3.5" aria-hidden /> Blocked by the sandbox:
          </span>
          {r.blockedActions.map((a) => (
            <Badge key={a} tone="warn" className="font-mono">
              {a}
            </Badge>
          ))}
        </div>
      )}
      {r.toolCalls.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted">Tool calls:</span>
          {r.toolCalls.map((t, k) => (
            <Badge key={k} className="font-mono">
              {t}
            </Badge>
          ))}
        </div>
      )}
      {r.output && (
        <details>
          <summary className="min-h-7 cursor-pointer text-xs text-muted hover:text-fg">Output</summary>
          <pre className="mt-1 max-h-56 overflow-auto rounded-md border border-border bg-surface p-2.5 font-sans text-[12.5px] whitespace-pre-wrap text-fg-2 scrollbar-thin">{r.output}</pre>
        </details>
      )}
    </div>
  );
}

function Tests({ draft, onRun, running, disabledReason }: { draft: AgentDraft; onRun: () => void; running: boolean; disabledReason: string }) {
  const results = new Map(draft.testResults.map((r) => [r.name, r]));
  const cases: DraftTestCase[] = draft.testCases.length ? draft.testCases : draft.testResults.map((r) => ({ name: r.name, input: r.input, expect: [] }) as unknown as DraftTestCase);
  const passed = draft.testResults.filter((r) => r.passed).length;
  const blocked = draft.testResults.reduce((n, r) => n + r.blockedActions.length, 0);
  return (
    <Card className="min-w-0 overflow-hidden" data-testid="draft-tests">
      <CardHeader
        icon={<FlaskConical className="size-4" />}
        title="Test cases"
        subtitle={
          draft.state === DraftState.TESTING
            ? "Running in the sandbox…"
            : draft.testResults.length
              ? `${passed}/${draft.testResults.length} passed${blocked ? ` · ${blocked} blocked ${blocked === 1 ? "action" : "actions"}` : ""}`
              : `${cases.length} ${cases.length === 1 ? "case" : "cases"} · not run yet`
        }
        actions={
          <Button size="sm" icon={<FlaskConical className="size-3.5" />} loading={running || draft.state === DraftState.TESTING} disabled={!!disabledReason} title={disabledReason || "Run every case in a sandbox (no external, spend or destructive actions)"} onClick={onRun} data-testid="draft-test">
            {draft.testResults.length ? "Run again" : "Test run"}
          </Button>
        }
      />
      {cases.length === 0 ? (
        <EmptyState compact icon={<FlaskConical />} title="No test cases" body="Refine the draft and ask for test cases." />
      ) : (
        <ol className="divide-y divide-border">
          {cases.map((c, i) => {
            const r = results.get(c.name);
            return (
              <li key={c.name || i} className="px-4 py-3">
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="text-[13px] font-semibold text-fg">{c.name || `Case ${i + 1}`}</span>
                  {!r && draft.state === DraftState.TESTING && (
                    <span className="inline-flex items-center gap-1 text-xs text-info">
                      <Loader2 className="size-3 animate-spin" aria-hidden /> running
                    </span>
                  )}
                </div>
                <p className="mt-0.5 text-[13px] text-fg-2">“{c.input}”</p>
                {c.expect.length > 0 && (
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {c.expect.map((e) => (
                      <Badge key={e} className="font-mono" title={/^(contains|not_contains|calls|no_calls|json)/.test(e) ? "Checked automatically" : "Listed for you to judge"}>
                        {!/^(contains|not_contains|calls|no_calls|json)/.test(e) && <CircleHelp className="size-3" aria-hidden />}
                        {e}
                      </Badge>
                    ))}
                  </div>
                )}
                {r && <TestResult r={r} />}
              </li>
            );
          })}
        </ol>
      )}
    </Card>
  );
}

function Progress({ draft }: { draft: AgentDraft }) {
  const notices = useDraftNotices(draft.id);
  const busy = BUSY_STATES.has(draft.state);
  const now = useNow(busy ? 1000 : 30_000);
  const recent = notices.slice(-4).reverse();
  if (!busy && recent.length === 0) return null;
  return (
    <Card className={cx("mb-5 p-4", busy && "border-info/35")} role="status" aria-live="polite" data-testid="draft-progress">
      <div className="flex items-center gap-2 text-[13px] font-semibold text-fg">
        {busy ? <Loader2 className="size-4 animate-spin text-info" aria-hidden /> : <CheckCircle2 className="size-4 text-ok" aria-hidden />}
        {draft.state === DraftState.DRAFTING
          ? `Designing revision ${draft.revision || 1}…`
          : draft.state === DraftState.TESTING
            ? "Running the test cases in a sandbox…"
            : "Progress"}
        {busy && <span className="font-normal text-muted tabular">· {timeAgo(tsMs(draft.updatedAt), now)}</span>}
      </div>
      {recent.length > 0 && (
        <ol className="mt-2 space-y-1">
          {recent.map((n) => (
            <li key={n.notice.id} className="flex gap-2 text-[12.5px]">
              <span className="w-[62px] shrink-0 text-muted tabular">{formatClock(new Date(n.at))}</span>
              <span className="min-w-0 text-fg-2">
                <span className="font-medium text-fg">{n.notice.title}</span>
                {n.notice.body && <span className="text-muted"> — {n.notice.body}</span>}
              </span>
            </li>
          ))}
        </ol>
      )}
      {draft.via && <p className="mt-2 text-[11.5px] text-muted">via {draft.via}</p>}
    </Card>
  );
}

function PublishCard({ draft, reason, onPublish, publishing }: { draft: AgentDraft; reason: string; onPublish: () => void; publishing: boolean }) {
  const approval = useEvents((s) => (draft.approvalId ? s.approvals[draft.approvalId] : undefined));
  const qc = useQueryClient();
  // The decision changes the draft (published / back to ready): refetch it at once.
  const decided = approval && approval.state !== ApprovalState.PENDING ? approval.state : 0;
  useEffect(() => {
    if (decided) void qc.invalidateQueries({ queryKey: draftKeys.draft(draft.id) });
  }, [decided, draft.id, qc]);
  const agentNote = draft.notes.find((n) => n.kind === "agent");
  return (
    <Card className={cx("p-4", draft.state === DraftState.PENDING_APPROVAL && "border-warn/40")} data-testid="draft-publish">
      <div className="mb-1 flex items-center gap-2 text-[13px] font-semibold text-fg">
        <Rocket className="size-4 text-accent" aria-hidden /> Publish
      </div>
      {draft.state === DraftState.PUBLISHED ? (
        <div className="space-y-2 text-[13px]">
          <p className="flex items-center gap-1.5 text-ok">
            <CheckCircle2 className="size-4" aria-hidden /> Published to the vault.
          </p>
          <ul className="space-y-1">
            {draft.notes.map((n) => (
              <li key={n.path}>
                <Link to={n.kind === "agent" ? `/agents/${n.id}` : n.kind === "team" ? `/teams/${n.id}` : `/vault?note=${encodeURIComponent(n.path)}`} className="inline-flex min-h-7 items-center gap-1 font-mono text-[12.5px] text-accent hover:underline">
                  {noteIcon(n.kind)} {n.path}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <>
          <p className="text-[12.5px] text-muted">Publishing asks for your approval; the notes are written only when it is granted (all or nothing).</p>
          {approval ? (
            <div className="mt-3">
              <ApprovalCard approval={approval} compact />
            </div>
          ) : draft.approvalId ? (
            <p className="mt-3 text-[12.5px] text-muted">Approval {draft.approvalId} requested — it is in your Inbox.</p>
          ) : null}
          {draft.state !== DraftState.PENDING_APPROVAL && (
            <div className="mt-3">
              <Button variant="primary" className="w-full justify-center" icon={<Rocket className="size-3.5" />} disabled={!!reason} loading={publishing} onClick={onPublish} aria-describedby={reason ? "publish-reason" : undefined} data-testid="draft-publish-btn">
                {agentNote?.exists ? "Publish update" : "Publish"}
              </Button>
              {reason && (
                <p id="publish-reason" className={cx("mt-2 flex items-start gap-1.5 text-[12.5px]", draft.problems.length ? "text-danger" : "text-muted")}>
                  {draft.problems.length > 0 && <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />}
                  {reason}
                </p>
              )}
            </div>
          )}
        </>
      )}
    </Card>
  );
}

function RefineCard({ draft, disabledReason }: { draft: AgentDraft; disabledReason: string }) {
  const [text, setText] = useState("");
  const refine = useRefineDraft();
  const submit = () => {
    const t = text.trim();
    if (!t || disabledReason) return;
    refine.mutate({ id: draft.id, feedback: t }, { onSuccess: () => setText("") });
  };
  return (
    <Card className="p-4">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label htmlFor="refine" className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-fg">
          <MessageSquarePlus className="size-4 text-accent" aria-hidden /> Refine with feedback
        </label>
        <Textarea
          id="refine"
          rows={3}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
          disabled={!!disabledReason}
          placeholder={draft.problems.length ? "e.g. It doesn't need to change code — drop the anthrex tools." : "e.g. Also check Amazon.es, and only notify me once per day."}
          className="resize-y"
          data-testid="draft-feedback"
        />
        <div className="mt-2 flex items-center gap-2">
          <span className="min-w-0 flex-1 text-xs text-muted">{disabledReason || `Makes revision ${draft.revision + 1}. ⌘↵ to send.`}</span>
          <Button type="submit" size="sm" icon={<Wrench className="size-3.5" />} loading={refine.isPending} disabled={!text.trim() || !!disabledReason}>
            Refine
          </Button>
        </div>
      </form>
      {draft.feedback.length > 0 && (
        <details className="mt-3 border-t border-border pt-2">
          <summary className="min-h-7 cursor-pointer text-xs text-muted hover:text-fg">Earlier feedback ({draft.feedback.length})</summary>
          <ol className="mt-1 list-decimal space-y-1 pl-5 text-[12.5px] text-fg-2">
            {draft.feedback.map((f, i) => (
              <li key={i}>{f}</li>
            ))}
          </ol>
        </details>
      )}
    </Card>
  );
}

export default function ArchitectDraft() {
  const { id = "" } = useParams();
  const q = useDraft(id);
  const test = useTestDraft();
  const publish = usePublishDraft();
  const discard = useDiscardDraft();
  const now = useNow(30_000);
  const d = q.data;

  if (!d) {
    if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
    return (
      <div className="space-y-4">
        <Skeleton className="h-9 w-80" />
        <Skeleton className="h-24" />
        <Skeleton className="h-[480px]" />
      </div>
    );
  }

  const busy = BUSY_STATES.has(d.state);
  const closed = d.state === DraftState.PUBLISHED || d.state === DraftState.DISCARDED;
  const publishReason = d.problems.length
    ? `Resolve the ${d.problems.length === 1 ? "problem" : `${d.problems.length} problems`} above first (refine the draft).`
    : busy
      ? "Wait until the current step has finished."
      : d.state === DraftState.FAILED
        ? "Drafting failed; refine to try again."
        : d.state === DraftState.READY || d.state === DraftState.PENDING_APPROVAL
          ? ""
          : "Not ready to publish.";
  const testReason = busy ? "Busy…" : d.problems.length ? "Fix the problems before testing." : closed ? "This draft is closed." : d.state === DraftState.PENDING_APPROVAL ? "Waiting for approval." : d.state === DraftState.FAILED ? "Drafting failed." : "";
  const refineReason = busy ? "Wait for the current step to finish." : closed ? "This draft is closed." : d.state === DraftState.PENDING_APPROVAL ? "Waiting for approval; deny it to keep refining." : "";
  const kindIcon = d.kind === DraftKind.TEAM ? <Users /> : d.kind === DraftKind.WORKFLOW ? <Workflow /> : <Bot />;

  return (
    <div data-testid="draft-view">
      <Link to="/architect" className="mb-3 inline-flex min-h-8 items-center gap-1 text-[13px] text-muted hover:text-fg">
        <ArrowLeft className="size-3.5" aria-hidden /> Architect
      </Link>
      <div className="mb-5 flex flex-wrap items-start gap-3">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent [&_svg]:size-5" aria-hidden>
          {kindIcon}
        </span>
        <div className="min-w-0 flex-1 basis-60">
          <h1 className="text-[22px] leading-tight font-semibold tracking-[-0.01em] break-words text-fg">{draftTitle(d)}</h1>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-muted">
            <Badge tone={draftStateTone[d.state]}>
              {busy && <Loader2 className="size-3 animate-spin" aria-hidden />}
              {draftStateLabel[d.state]}
            </Badge>
            <Badge>{draftKindLabel[d.kind]}</Badge>
            <span>revision {d.revision}</span>
            <span>· updated {timeAgo(tsMs(d.updatedAt), now)}</span>
            {d.updateAgentId && (
              <span>
                · improves{" "}
                <Link to={`/agents/${d.updateAgentId}`} className="text-accent hover:underline">
                  {d.updateAgentId}
                </Link>
              </span>
            )}
          </div>
        </div>
        {!closed && (
          <ConfirmButton variant="ghost" icon={<Trash2 className="size-3.5" />} label="Discard" confirmLabel="Confirm discard" loading={discard.isPending} onConfirm={() => discard.mutate(d.id)} />
        )}
      </div>

      <Progress draft={d} />

      {d.error && (
        <div role="alert" className="mb-5 flex items-start gap-2 rounded-xl border border-danger/40 bg-danger-soft px-4 py-3 text-[13px] text-danger">
          <XCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>{d.error}</span>
        </div>
      )}

      {d.problems.length > 0 && (
        <section role="alert" aria-labelledby="problems-h" data-testid="draft-problems" className="mb-5 rounded-xl border-2 border-danger/60 bg-danger-soft p-4">
          <h2 id="problems-h" className="flex items-center gap-2 text-[15px] font-semibold text-danger">
            <ShieldAlert className="size-5" aria-hidden />
            {d.problems.length === 1 ? "1 problem blocks publishing" : `${d.problems.length} problems block publishing`}
          </h2>
          <p className="mt-0.5 text-[12.5px] text-fg-2">Validation could not fix these automatically. Refine the draft with feedback to resolve them.</p>
          <ul className="mt-2.5 space-y-1.5">
            {d.problems.map((p) => (
              <li key={p} className="flex items-start gap-2 rounded-lg bg-surface px-3 py-2 text-[13px] text-fg">
                <AlertTriangle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
                <span className="min-w-0 break-words">
                  <RichLine text={p} />
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {d.fixes.length > 0 && (
        <Card className="mb-5 p-4">
          <h2 className="flex items-center gap-2 text-[13px] font-semibold text-fg">
            <Wrench className="size-4 text-info" aria-hidden /> Validation fixed {d.fixes.length} {d.fixes.length === 1 ? "thing" : "things"} automatically
          </h2>
          <ul className="mt-2 space-y-1">
            {d.fixes.map((f) => (
              <li key={f} className="flex items-start gap-2 text-[13px] text-fg-2">
                <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-ok" aria-hidden />
                <span className="min-w-0 break-words">
                  <RichLine text={f} />
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <div className="flex flex-col gap-5 lg:grid lg:grid-cols-[minmax(0,1fr)_360px] lg:items-start">
        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-5">
          <div className="order-2 min-w-0 lg:order-none">
            {d.state === DraftState.DRAFTING && d.notes.length === 0 ? (
              <Card className="p-4">
                <Skeleton className="mb-3 h-5 w-48" />
                <Skeleton className="h-64" />
              </Card>
            ) : (
              <Notes notes={d.notes} />
            )}
          </div>
          <div className="order-4 min-w-0 lg:order-none">
            <Tests draft={d} running={test.isPending} disabledReason={testReason} onRun={() => test.mutate(d.id)} />
          </div>
        </div>
        <div className="contents lg:flex lg:min-w-0 lg:flex-col lg:gap-5">
          <div className="order-1 lg:order-none">
            <PublishCard draft={d} reason={publishReason} publishing={publish.isPending} onPublish={() => publish.mutate(d.id)} />
          </div>
          {(d.rationale || d.risks.length > 0) && (
            <Card className="order-3 p-4 lg:order-none">
              {d.rationale && (
                <>
                  <h2 className="mb-1 flex items-center gap-2 text-[13px] font-semibold text-fg">
                    <Lightbulb className="size-4 text-accent" aria-hidden /> Why this design
                  </h2>
                  <Markdown className="text-[13px]">{d.rationale}</Markdown>
                </>
              )}
              {d.risks.length > 0 && (
                <>
                  <h2 className={cx("mb-1 flex items-center gap-2 text-[13px] font-semibold text-warn", d.rationale && "mt-4")}>
                    <AlertTriangle className="size-4" aria-hidden /> Risks
                  </h2>
                  <ul className="list-disc space-y-1 pl-5 text-[13px] text-fg-2">
                    {d.risks.map((r) => (
                      <li key={r}>{r}</li>
                    ))}
                  </ul>
                </>
              )}
            </Card>
          )}
          <div className="order-5 lg:order-none">
            <RefineCard draft={d} disabledReason={refineReason} />
          </div>
          <Card className="order-6 p-4 lg:order-none">
            <div className="mb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">Request</div>
            <p className="text-[13px] whitespace-pre-wrap text-fg-2">{d.request}</p>
            {d.context && <p className="mt-2 text-[12.5px] whitespace-pre-wrap text-muted">{d.context}</p>}
            <div className="mt-2 flex flex-wrap gap-x-2 text-[11.5px] text-muted">
              <span className="font-mono">{d.id}</span>
              {d.via && <span>· {d.via}</span>}
              {d.taskId && (
                <Link to={`/tasks/${d.taskId}`} className="text-accent hover:underline">
                  · drafting task
                </Link>
              )}
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}
