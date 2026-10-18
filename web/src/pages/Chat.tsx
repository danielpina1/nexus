import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { AlertCircle, AlertTriangle, ArrowRight, BookOpen, Compass, PanelRight, Sparkles, Trash2, Wand2 } from "lucide-react";
import { useChat, type ChatMessage } from "../lib/chat";
import { useChildren, useTask, useTimeline } from "../lib/selectors";
import { useTaskHistory } from "../lib/events";
import { useCancelTask } from "../lib/queries";
import { useNow } from "../lib/useNow";
import { BACKGROUND_WARNING, engineModelLabel, runModelFrom, untrackedBackgroundWarning } from "../lib/runMeta";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { CommandKind } from "../gen/nexus/v1/command_pb";
import { CommandBar } from "../components/CommandBar";
import { InlineApproval, InlineQuestion, RunView, ToolCallRow } from "../components/RunView";
import { buildItems, type RunItem } from "../lib/runItems";
import { StreamingMarkdown } from "../components/StreamingMarkdown";
import { ArtifactView } from "../components/Artifact";
import { Markdown } from "../components/Markdown";
import { Button } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Tabs } from "../components/ui/Tabs";
import { agentName, useAgentMap } from "../components/agentMeta";
import { cx, isActive, tsMs } from "../lib/format";

const kindLabel: Record<CommandKind, string> = {
  [CommandKind.UNSPECIFIED]: "command",
  [CommandKind.SEARCH]: "search",
  [CommandKind.CAPTURE]: "capture",
  [CommandKind.TASK]: "task",
  [CommandKind.WORKFLOW]: "workflow",
};

/** Plain text of a reply's streamed text items. */
function replyText(items: RunItem[]): string {
  return items
    .filter((it): it is Extract<RunItem, { k: "text" }> => it.k === "text")
    .map((it) => it.text)
    .join("\n\n")
    .trim();
}

function Thinking({ since, label = "Thinking…" }: { since: number; label?: string }) {
  const now = useNow(1000);
  const secs = Math.max(0, Math.floor((now - since) / 1000));
  return (
    <div className="flex h-8 items-center gap-2 px-1 text-[13px] text-muted" role="status" data-testid="chat-thinking">
      <span className="flex gap-1" aria-hidden>
        {[0, 1, 2].map((i) => (
          <span key={i} className="size-1.5 animate-bounce rounded-full bg-muted motion-reduce:animate-none" style={{ animationDelay: `${i * 120}ms` }} />
        ))}
      </span>
      {label} <span className="tabular">{secs}s</span>
    </div>
  );
}

/**
 * The assistant's reply rendered inline from the run's events (shared event store): streamed
 * Markdown with a caret, compact tool chips, inline questions and approvals.
 */
function ChatReply({ m, live, onDetails }: { m: ChatMessage; live: boolean; onDetails: () => void }) {
  const taskId = m.taskId!;
  useTaskHistory(live ? taskId : undefined);
  const task = useTask(taskId);
  const events = useTimeline(taskId);
  const agents = useAgentMap();
  const children = useChildren(taskId);
  const annotate = useChat((s) => s.annotate);
  const items = useMemo(() => buildItems(events, taskId), [events, taskId]);
  const model = useMemo(() => runModelFrom(events, task), [events, task]);
  const active = task ? isActive(task.state) : live && !m.finalText;
  const text = replyText(items);
  const hasText = items.some((it) => it.k === "text" && it.text.trim());

  // Send → first visible token, and the final answer + engine once done.
  useEffect(() => {
    if (live && hasText && m.firstTokenMs === undefined) {
      const ms = Date.now() - m.at;
      console.info(`[chat] first token after ${ms} ms (task ${taskId})`);
      annotate(m.id, { firstTokenMs: ms });
    }
  }, [live, hasText, m.firstTokenMs, m.at, m.id, taskId, annotate]);
  const engine = engineModelLabel(model);
  useEffect(() => {
    if (task && !isActive(task.state) && (m.finalText !== (text || task.summary) || (engine && m.engine !== engine))) {
      annotate(m.id, { finalText: text || task.summary, engine: engine || m.engine });
    }
  }, [task, text, engine, m.finalText, m.engine, m.id, annotate]);

  const agent = task ? agents[task.agentId] : undefined;
  const who = task ? agentName(agents, task.agentId, task.workflowId) : "Nexus";
  const label = [who, engine || m.engine].filter(Boolean).join(" · ");
  const waiting = task?.state === TaskState.WAITING_INPUT || task?.state === TaskState.WAITING_APPROVAL || task?.state === TaskState.NEEDS_INPUT;
  const lastAt = events.length ? tsMs(events[events.length - 1].at) || m.at : m.at;
  const failed = task?.state === TaskState.FAILED;
  const cancelled = task?.state === TaskState.CANCELLED;
  const activeKids = children.filter((c) => isActive(c.state)).length;
  const warn = untrackedBackgroundWarning(text || task?.summary || "", active, activeKids);

  // Without live events (an old message), show what was kept.
  const fallback = !items.length && !active ? m.finalText || task?.summary || "" : "";

  return (
    <div className="min-w-0 space-y-2" data-testid="chat-reply" data-state={task ? TaskState[task.state] : "PENDING"}>
      <div className="flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted" data-testid="chat-reply-label">
        <span className="font-medium text-fg-2" title={agent?.model ? `Agent model alias: ${agent.model}` : undefined}>
          {label}
        </span>
        {m.firstTokenMs !== undefined && <span className="tabular" title="Time from send to the first streamed token">· first token {(m.firstTokenMs / 1000).toFixed(1)}s</span>}
      </div>
      {items.length === 0 && active && <Thinking since={m.at} />}
      {items.map((it, idx) => {
        const lastItem = idx === items.length - 1;
        switch (it.k) {
          case "text":
            return it.text.trim() ? (
              <div key={it.key} className="rounded-2xl rounded-tl-md border border-border bg-surface px-3.5 py-2 text-[14px] shadow-card" data-testid="chat-bubble">
                <StreamingMarkdown text={it.text} streaming={lastItem && active} />
              </div>
            ) : null;
          case "tool":
            return <ToolCallRow key={it.key} call={it.call} result={it.result} streaming={active} compact />;
          case "approval":
            return <InlineApproval key={it.key} id={it.id} />;
          case "question":
            return <InlineQuestion key={it.key} qid={it.qid} ev={it.ev} />;
          case "artifact":
            return <ArtifactView key={it.key} att={it.att} />;
          default:
            return null;
        }
      })}
      {items.length > 0 && active && !waiting && items[items.length - 1].k !== "text" && <Thinking since={lastAt} label="Working…" />}
      {waiting && (
        <div className="px-1 text-[12.5px] text-muted" role="status">
          {task?.state === TaskState.WAITING_APPROVAL ? "Waiting for your approval…" : "Waiting for your answer…"}
        </div>
      )}
      {fallback && !failed && (
        <div className="rounded-2xl rounded-tl-md border border-border bg-surface px-3.5 py-2 text-[14px] shadow-card" data-testid="chat-bubble">
          <Markdown>{fallback}</Markdown>
        </div>
      )}
      {!active && !hasText && !fallback && !failed && !cancelled && task?.summary && (
        <div className="rounded-2xl rounded-tl-md border border-border bg-surface px-3.5 py-2 text-[14px] shadow-card">
          <Markdown>{task.summary}</Markdown>
        </div>
      )}
      {failed && (
        <div role="alert" className="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-soft px-3 py-2 text-[13px] text-danger">
          <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 break-words">{task?.summary || "The run failed."}</span>
        </div>
      )}
      {cancelled && <div className="text-[12px] text-muted">Stopped.</div>}
      {warn && (
        <div role="status" className="flex items-start gap-2 rounded-xl border border-warn/40 bg-warn-soft px-3 py-2 text-[12.5px] text-fg" data-testid="background-warning">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden />
          <span>{BACKGROUND_WARNING}</span>
        </div>
      )}
      {activeKids > 0 && (
        <div className="text-[12px] text-muted">
          {activeKids} sub-task{activeKids === 1 ? "" : "s"} still running
        </div>
      )}
      <button onClick={onDetails} className="inline-flex min-h-7 items-center gap-1 rounded-md px-1 text-[11.5px] text-muted hover:text-fg focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:outline-none pointer-coarse:min-h-11">
        <PanelRight className="size-3" aria-hidden /> Run details
      </button>
    </div>
  );
}

function Message({ m, live, retryInput }: { m: ChatMessage; live: boolean; retryInput?: string }) {
  const agents = useAgentMap();
  const setActive = useChat((s) => s.setActiveTask);
  const send = useChat((s) => s.send);
  if (m.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-md bg-primary px-3.5 py-2 text-[14px] whitespace-pre-wrap text-primary-fg">{m.text}</div>
      </div>
    );
  }
  const r = m.route;
  const showRoute = r && (r.kind === CommandKind.SEARCH || r.kind === CommandKind.CAPTURE || r.kind === CommandKind.WORKFLOW);
  return (
    <div className="flex gap-2.5">
      <div className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent" aria-hidden>
        <Sparkles className="size-3.5" />
      </div>
      <div className="min-w-0 max-w-[90%] flex-1">
        {showRoute && (
          <div className="mb-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted">
            <Compass className="size-3" />
            <span>{kindLabel[r.kind]}</span>
          </div>
        )}
        {m.taskId ? (
          <ChatReply m={m} live={live} onDetails={() => setActive(m.taskId)} />
        ) : m.pending ? (
          <Thinking since={m.at} />
        ) : m.error ? (
          <div role="alert" className="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-soft px-3 py-2 text-[13px] text-danger">
            <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span className="min-w-0 flex-1 break-words">{m.error}</span>
            {retryInput && (
              <button onClick={() => void send(retryInput, { source: "chat" })} className="-my-0.5 shrink-0 rounded-md px-2 py-0.5 text-xs font-semibold hover:bg-danger/10">
                Retry
              </button>
            )}
          </div>
        ) : m.text || m.choices?.length || m.sources?.length ? (
          <div className="rounded-2xl rounded-tl-md border border-border bg-surface px-3.5 py-2 text-[14px] shadow-card">
            <Markdown>{m.text}</Markdown>
            {m.choices && m.choices.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {m.choices.map((c) => (
                  <Button key={c} size="xs" variant="secondary" onClick={() => void send(m.originalInput ?? "", { agentId: c, source: "chat" })}>
                    {agentName(agents, c)}
                  </Button>
                ))}
              </div>
            )}
            {m.sources && m.sources.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {m.sources.map((s) => (
                  <Link key={s.id || s.path} to={`/vault?note=${encodeURIComponent(s.id || s.path)}`} className="inline-flex items-center gap-1 rounded-md bg-accent-soft px-1.5 py-0.5 text-xs text-accent hover:underline">
                    <BookOpen className="size-3" />
                    {s.title || s.path}
                  </Link>
                ))}
              </div>
            )}
          </div>
        ) : null}
        {m.draftId && (
          <Link
            to={`/architect/${m.draftId}`}
            className="mt-1 flex w-full items-center gap-2.5 rounded-xl border border-border bg-surface px-3 py-2 text-left transition-colors hover:border-border-strong"
          >
            <span className="flex size-6 items-center justify-center rounded-md bg-accent-soft text-accent" aria-hidden>
              <Wand2 className="size-3.5" />
            </span>
            <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-fg">Open the agent draft</span>
            <ArrowRight className="size-3.5 text-muted" aria-hidden />
          </Link>
        )}
      </div>
    </div>
  );
}

function ContextPane({ taskId, messages }: { taskId?: string; messages: ChatMessage[] }) {
  const tl = useTimeline(taskId);
  const used = useMemo(() => {
    const out = new Set<string>();
    for (const e of tl) {
      if (e.body.case === "toolResult") {
        const notes = (e.body.value.output as { notes?: unknown } | undefined)?.notes;
        if (Array.isArray(notes)) for (const n of notes) if (typeof n === "string") out.add(n);
      }
    }
    for (const m of messages) if (m.taskId === taskId) for (const s of m.sources ?? []) out.add(s.title || s.path);
    return [...out];
  }, [tl, messages, taskId]);
  const msg = messages.find((m) => m.taskId === taskId);
  const agents = useAgentMap();
  return (
    <div className="space-y-5 p-4 text-[13px]">
      <div>
        <div className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Routing</div>
        {msg?.route ? (
          <div className="space-y-1 text-fg-2">
            <div>
              <span className="text-muted">Agent </span>
              {agentName(agents, msg.route.agentId)}
            </div>
            <div>
              <span className="text-muted">Confidence </span>
              {Math.round(msg.route.confidence * 100)}%
            </div>
            {msg.route.reason && <div className="text-xs text-muted">{msg.route.reason}</div>}
            {msg.route.alternatives.length > 0 && <div className="text-xs text-muted">Alternatives: {msg.route.alternatives.map((a) => agentName(agents, a)).join(", ")}</div>}
          </div>
        ) : (
          <div className="text-xs text-muted">—</div>
        )}
      </div>
      <div>
        <div className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Notes used</div>
        {used.length === 0 ? (
          <div className="text-xs text-muted">No notes yet.</div>
        ) : (
          <div className="flex flex-col gap-1">
            {used.map((n) => (
              <Link key={n} to={`/vault?note=${encodeURIComponent(n)}`} className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-fg-2 hover:bg-surface-2 hover:text-fg">
                <BookOpen className="size-3.5 text-muted" />
                <span className="truncate">{n}</span>
              </Link>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export default function Chat() {
  const messages = useChat((s) => s.messages);
  const activeTaskId = useChat((s) => s.activeTaskId);
  const clear = useChat((s) => s.clear);
  const task = useTask(activeTaskId);
  const lastReply = [...messages].reverse().find((m) => m.role === "nexus");
  const lastTask = useTask(lastReply?.taskId);
  const busy = !!lastReply && (lastReply.pending || (!!lastTask && isActive(lastTask.state)));
  const cancel = useCancelTask();
  const end = useRef<HTMLDivElement>(null);
  const [mobilePane, setMobilePane] = useState<"chat" | "run">("chat");
  const [showContext, setShowContext] = useState(true);

  // Follow the streaming reply (content grows without new messages).
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useEffect(() => {
    const el = scroller.current;
    const inner = end.current?.parentElement;
    if (!el || !inner) return;
    el.scrollTop = el.scrollHeight;
    const ro = new ResizeObserver(() => {
      if (stick.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(inner);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    stick.current = true;
    end.current?.scrollIntoView({ block: "end" });
  }, [messages.length]);

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 lg:hidden">
        <Tabs<"chat" | "run">
          value={mobilePane}
          onChange={setMobilePane}
          items={[
            { id: "chat", label: "Conversation" },
            { id: "run", label: "Live run" },
          ]}
        />
      </div>
      <div className="flex min-h-0 flex-1">
        <section className={cx("min-w-0 flex-1 flex-col border-border lg:flex lg:max-w-[560px] lg:border-r xl:max-w-[620px]", mobilePane === "chat" ? "flex" : "hidden")}>
          <div className="hidden items-center gap-2 border-b border-border px-4 py-3 lg:flex">
            <h1 className="text-[15px] font-semibold text-fg">Chat</h1>
            <span className="text-xs text-muted">with your assistant · @agent, /search, remember …</span>
            <span className="flex-1" />
            {messages.length > 0 && (
              <Button size="xs" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={clear} title="Start a new conversation">
                New chat
              </Button>
            )}
          </div>
          <div
            ref={scroller}
            onScroll={(e) => {
              const el = e.currentTarget;
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 64;
            }}
            className="min-h-0 flex-1 overflow-y-auto px-4 py-5 scrollbar-thin"
            aria-live="polite"
          >
            <div className="space-y-4">
            {messages.length === 0 ? (
              <EmptyState
                icon={<Sparkles />}
                title="Chat with Nexus"
                body="Your assistant answers here, live. Ask what Nexus can do, hand it work, or start with @agent to talk to a specialist. /search searches your vault; remember … saves a note."
              />
            ) : (
              messages.map((m, i) => (
                <Message key={m.id} m={m} live={i >= messages.length - 6} retryInput={m.error && messages[i - 1]?.role === "user" ? messages[i - 1].text : undefined} />
              ))
            )}
            <div ref={end} />
            </div>
          </div>
          <div className="border-t border-border px-3 pt-3 pb-2">
            <CommandBar
              autoFocus
              source="chat"
              busy={busy}
              onStop={lastTask && isActive(lastTask.state) ? () => cancel.mutate(lastTask.id) : undefined}
              onSent={() => setMobilePane("chat")}
              suggestions={messages.length === 0}
              placeholder={busy ? "Nexus is answering…" : "Message Nexus…"}
            />
          </div>
        </section>

        <section className={cx("min-w-0 flex-1 flex-col bg-surface lg:flex", mobilePane === "run" ? "flex" : "hidden")}>
          {task ? (
            <RunView task={task} embedded />
          ) : (
            <EmptyState className="h-full" icon={<PanelRight />} title="No run yet" body="When a message starts a task, its run streams here live: output, tool calls, approvals and cost." />
          )}
        </section>

        {task && (
          <aside className={cx("hidden w-64 shrink-0 border-l border-border bg-bg", showContext && "2xl:block")}>
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <span className="text-[13px] font-semibold text-fg">Context</span>
              <button className="text-xs text-muted hover:text-fg" onClick={() => setShowContext(false)}>
                Hide
              </button>
            </div>
            <ContextPane taskId={activeTaskId} messages={messages} />
          </aside>
        )}
      </div>
    </div>
  );
}
