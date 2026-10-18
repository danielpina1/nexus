import { useState } from "react";
import { useNavigate } from "react-router";
import { ArrowUp, Sparkles } from "lucide-react";
import { useChat } from "../lib/chat";
import { cx } from "../lib/format";
import { Kbd } from "./ui/Input";

const SUGGESTIONS = ["buy the usual groceries", "is the vps ok?", "fix the failing lint in nexus", "what do I know about Rui?", "remember Ana's birthday is May 3"];

/** The one input for everything: sends to CommandService.Execute via the chat store. */
export function CommandBar({ autoFocus, className, onSent, suggestions = true }: { autoFocus?: boolean; className?: string; onSent?: () => void; suggestions?: boolean }) {
  const [text, setText] = useState("");
  const send = useChat((s) => s.send);
  const navigate = useNavigate();
  const submit = (value: string) => {
    const v = value.trim();
    if (!v) return;
    setText("");
    void send(v);
    if (onSent) onSent();
    else navigate("/chat");
  };
  return (
    <div className={className}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit(text);
        }}
        className="group flex items-center gap-2 rounded-2xl border border-border bg-surface p-1.5 pl-3.5 shadow-card transition-shadow focus-within:border-accent/50 focus-within:ring-4 focus-within:ring-ring/15"
      >
        <Sparkles className="size-4 shrink-0 text-accent" />
        <input
          data-command-input
          autoFocus={autoFocus}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Ask or tell Nexus anything…"
          className="h-9 min-w-0 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-muted"
        />
        <Kbd className="hidden sm:inline-flex">/</Kbd>
        <button
          type="submit"
          disabled={!text.trim()}
          className={cx("flex size-9 shrink-0 items-center justify-center rounded-xl transition-colors", text.trim() ? "bg-accent text-accent-fg" : "bg-surface-2 text-muted")}
          aria-label="Send"
        >
          <ArrowUp className="size-4" />
        </button>
      </form>
      {suggestions && (
        <div className="mt-2 flex gap-1.5 overflow-x-auto pb-1 scrollbar-thin">
          {SUGGESTIONS.map((s) => (
            <button key={s} onClick={() => submit(s)} className="shrink-0 rounded-full border border-border bg-surface px-2.5 py-1 text-xs text-fg-2 transition-colors hover:border-border-strong hover:text-fg">
              {s}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
