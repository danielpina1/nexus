import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { AlertCircle, ArrowRight, BookOpen, Compass, PanelRight, Sparkles, Trash2 } from "lucide-react";
import { useChat, type ChatMessage } from "../lib/chat";
import { useTask, useTimeline } from "../lib/selectors";
import { CommandKind } from "../gen/nexus/v1/command_pb";
import { CommandBar } from "../components/CommandBar";
import { RunView } from "../components/RunView";
import { Markdown } from "../components/Markdown";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge } from "../components/StateBadge";
import { Button } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Tabs } from "../components/ui/Tabs";
import { agentName, useAgentMap } from "../components/agentMeta";
import { cx, tierLabel } from "../lib/format";

const kindLabel: Record<CommandKind, string> = {
  [CommandKind.UNSPECIFIED]: "command",
  [CommandKind.SEARCH]: "search",
  [CommandKind.CAPTURE]: "capture",
  [CommandKind.TASK]: "task",
  [CommandKind.WORKFLOW]: "workflow",
};

function TaskChip({ taskId, active, onClick }: { taskId: string; active: boolean; onClick: () => void }) {
  const task = useTask(taskId);
  if (!task) return null;
  return (
    <button
      onClick={onClick}
      className={cx(
        "mt-1 flex w-full items-center gap-2.5 rounded-xl border px-3 py-2 text-left transition-colors",
        active ? "border-accent/50 bg-accent-soft" : "border-border bg-surface hover:border-border-strong",
      )}
    >
      <AgentIcon agentId={task.agentId} size="sm" />
      <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-fg">{task.title}</span>
      <StateBadge state={task.state} />
    </button>
  );
}

function Message({ m, activeTaskId, retryInput }: { m: ChatMessage; activeTaskId?: string; retryInput?: string }) {
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
  return (
    <div className="flex gap-2.5">
      <div className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent" aria-hidden>
        <Sparkles className="size-3.5" />
      </div>
      <div className="min-w-0 max-w-[90%] flex-1">
        {r && (
          <div className="mb-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted">
            <Compass className="size-3" />
            <span>{kindLabel[r.kind]}</span>
            {r.agentId && (
              <>
                <ArrowRight className="size-3" />
                <span className="font-medium text-fg-2">{agentName(agents, r.agentId)}</span>
              </>
            )}
            {r.tier ? <span>· {tierLabel[r.tier]}</span> : null}
            <span className={cx("tabular", r.confidence < 0.75 && "text-warn")}>· {Math.round(r.confidence * 100)}%</span>
            {r.reason && <span className="hidden truncate sm:inline">· {r.reason}</span>}
          </div>
        )}
        {m.pending ? (
          <div className="flex h-8 items-center gap-1 px-1">
            {[0, 1, 2].map((i) => (
              <span key={i} className="size-1.5 animate-bounce rounded-full bg-muted" style={{ animationDelay: `${i * 120}ms` }} />
            ))}
          </div>
        ) : m.error ? (
          <div role="alert" className="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-soft px-3 py-2 text-[13px] text-danger">
            <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span className="min-w-0 flex-1 break-words">{m.error}</span>
            {retryInput && (
              <button onClick={() => void send(retryInput)} className="-my-0.5 shrink-0 rounded-md px-2 py-0.5 text-xs font-semibold hover:bg-danger/10">
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
                  <Button key={c} size="xs" variant="secondary" onClick={() => void send(m.originalInput ?? "", { agentId: c })}>
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
        {m.taskId && <TaskChip taskId={m.taskId} active={m.taskId === activeTaskId} onClick={() => setActive(m.taskId)} />}
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
  const end = useRef<HTMLDivElement>(null);
  const [mobilePane, setMobilePane] = useState<"chat" | "run">("chat");
  const [showContext, setShowContext] = useState(true);

  useEffect(() => {
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
            <span className="text-xs text-muted">auto-routed · @agent to pick one</span>
            <span className="flex-1" />
            {messages.length > 0 && (
              <Button size="xs" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={clear}>
                Clear
              </Button>
            )}
          </div>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-5 scrollbar-thin">
            {messages.length === 0 ? (
              <EmptyState
                icon={<Sparkles />}
                title="Ask Nexus anything"
                body="Questions about your notes, things to remember, or tasks for your agents. The router picks the agent and machine; start with @agent to choose yourself."
              />
            ) : (
              messages.map((m, i) => <Message key={m.id} m={m} activeTaskId={activeTaskId} retryInput={m.error && messages[i - 1]?.role === "user" ? messages[i - 1].text : undefined} />)
            )}
            <div ref={end} />
          </div>
          <div className="border-t border-border px-3 pt-3 pb-2">
            <CommandBar autoFocus onSent={() => setMobilePane("chat")} suggestions={messages.length === 0} placeholder="Message Nexus…  @ picks an agent" />
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
