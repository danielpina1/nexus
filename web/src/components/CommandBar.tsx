import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { ArrowUp, Square } from "lucide-react";
import { useChat } from "../lib/chat";
import { useAgents } from "../lib/queries";
import { isMock } from "../lib/api";
import { cx } from "../lib/format";
import { fuzzyScore } from "../lib/fuzzy";
import { Kbd } from "./ui/Input";
import { AgentIcon } from "./AgentIcon";

const DEMO_SUGGESTIONS = ["buy the usual groceries", "is the vps ok?", "fix the failing lint in nexus", "what do I know about Rui?", "remember Ana's birthday is May 3"];

/**
 * The one input for everything: sends to CommandService.Execute via the chat store.
 * "@agent …" picks an agent explicitly (with inline completion).
 */
export function CommandBar({
  autoFocus,
  className,
  onSent,
  suggestions = true,
  placeholder = "Ask or tell Nexus anything…",
  source = "command-bar",
  busy,
  onStop,
}: {
  autoFocus?: boolean;
  className?: string;
  onSent?: () => void;
  suggestions?: boolean;
  placeholder?: string;
  /** "chat" on the Chat page (hub-side chat routing); the command bar elsewhere uses the router. */
  source?: "chat" | "command-bar";
  /** A reply is streaming: Enter does not send; the button stops the reply when onStop is set. */
  busy?: boolean;
  onStop?: () => void;
}) {
  const [text, setText] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const send = useChat((s) => s.send);
  const navigate = useNavigate();
  const { data: agents } = useAgents();
  const mention = /^@(\S*)$/.exec(text);
  const options = useMemo(() => {
    if (!mention) return [];
    return (agents ?? [])
      .filter((a) => a.enabled !== false)
      .map((a) => ({ a, s: fuzzyScore(`${a.id} ${a.name}`, mention[1]) }))
      .filter((x) => x.s > 0)
      .sort((x, y) => y.s - x.s)
      .slice(0, 6)
      .map((x) => x.a);
  }, [agents, mention?.[1]]); // eslint-disable-line react-hooks/exhaustive-deps

  const submit = (value: string) => {
    const v = value.trim();
    if (!v || busy) return;
    setText("");
    void send(v, { source });
    if (onSent) onSent();
    else navigate("/chat");
  };
  const complete = (id: string) => {
    setText(`@${id} `);
    setSel(0);
    inputRef.current?.focus();
  };

  const chips = isMock() ? DEMO_SUGGESTIONS : (agents ?? []).filter((a) => a.enabled !== false).slice(0, 6).map((a) => `@${a.id} `);

  return (
    <div className={cx("relative", className)}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (options.length && mention) complete(options[Math.min(sel, options.length - 1)].id);
          else submit(text);
        }}
        className="group flex items-center gap-2 rounded-xl border border-border-input bg-surface p-1.5 pl-4 shadow-card transition-[border-color,box-shadow] duration-200 focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/40"
      >
        <input
          ref={inputRef}
          data-command-input
          data-testid="command-input"
          autoFocus={autoFocus}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (!options.length) return;
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(options.length - 1, s + 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(0, s - 1));
            } else if (e.key === "Tab") {
              e.preventDefault();
              complete(options[Math.min(sel, options.length - 1)].id);
            }
          }}
          placeholder={placeholder}
          aria-label="Ask or tell Nexus"
          autoComplete="off"
          enterKeyHint="send"
          className="h-10 min-w-0 flex-1 bg-transparent text-base text-fg placeholder:text-muted focus-visible:outline-none sm:text-[14px]"
        />
        <span className="hidden sm:inline-flex"><Kbd>/</Kbd></span>
        {busy && onStop ? (
          <button
            type="button"
            onClick={onStop}
            data-testid="chat-stop"
            className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-surface-2 text-fg transition-colors duration-150 hover:bg-surface-3 focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:outline-none pointer-coarse:size-11"
            aria-label="Stop the reply"
            title="Stop"
          >
            <Square className="size-3.5 fill-current" />
          </button>
        ) : (
        <button
          type="submit"
          disabled={!text.trim() || busy}
          className={cx(
            "flex size-10 shrink-0 items-center justify-center rounded-lg transition-colors duration-150 pointer-coarse:size-11",
            text.trim() ? "bg-primary text-primary-fg hover:opacity-90" : "bg-surface-2 text-muted",
          )}
          aria-label="Send"
        >
          <ArrowUp className="size-4" />
        </button>
        )}
      </form>
      {options.length > 0 && (
        <div role="listbox" aria-label="Agents" className="animate-fade-up absolute inset-x-0 top-full z-30 mt-1.5 overflow-hidden rounded-xl border border-border bg-surface p-1 shadow-pop">
          {options.map((a, i) => (
            <button
              key={a.id}
              role="option"
              aria-selected={i === sel}
              type="button"
              onMouseMove={() => setSel(i)}
              onClick={() => complete(a.id)}
              className={cx("flex min-h-10 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-[13.5px]", i === sel ? "bg-surface-2 text-fg" : "text-fg-2")}
            >
              <AgentIcon agentId={a.id} icon={a.icon} size="sm" />
              <span className="font-medium">@{a.id}</span>
              <span className="min-w-0 flex-1 truncate text-muted">{a.name}</span>
            </button>
          ))}
        </div>
      )}
      {suggestions && chips.length > 0 && (
        <div className="mt-2.5 flex gap-1.5 overflow-x-auto pb-1 scrollbar-thin">
          {chips.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => (s.startsWith("@") ? complete(s.slice(1).trim()) : submit(s))}
              className="h-8 shrink-0 rounded-full border border-border bg-surface px-3 text-xs text-fg-2 transition-colors duration-150 hover:border-border-strong hover:text-fg pointer-coarse:h-10"
            >
              {s.trim()}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
