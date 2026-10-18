import { useState } from "react";
import { ChevronRight, CreditCard, FilePen, Search } from "lucide-react";
import { cx } from "../lib/format";
import { clip, diffStats, friendlyToolName, lineDiff, normalizeTool, str } from "../lib/tools";
import { DiffView } from "./DiffView";

/**
 * Human-readable rendering of tool calls, shared by the chat bubble chips, the run timeline and the
 * approval cards (one renderer per tool family; raw JSON only behind "Raw details").
 *
 * Tool names arrive as Nexus names ("memory.search"), Claude Code built-ins ("Bash", "Edit"),
 * Claude Code MCP names ("mcp__nexus__memory_search", "mcp__github__create_issue") or Codex names
 * ("shell", "apply_patch", "nexus.memory.search").
 */

type Input = Record<string, unknown>;

// ---------------------------------------------------------------- renderers

function Code({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <pre className={cx("max-h-60 overflow-auto rounded-lg border border-border bg-surface-2 p-2.5 font-mono text-[12px] leading-[18px] whitespace-pre-wrap break-words text-fg scrollbar-thin", className)}>
      {children}
    </pre>
  );
}

function KeyValues({ input }: { input: Input }) {
  const entries = Object.entries(input).filter(([, v]) => v !== undefined && v !== null && v !== "");
  if (!entries.length) return <div className="text-[12.5px] text-muted">No inputs.</div>;
  return (
    <dl className="grid grid-cols-[minmax(80px,max-content)_1fr] gap-x-3 gap-y-1 text-[12.5px]">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-mono text-muted">{k}</dt>
          <dd className="min-w-0 break-words whitespace-pre-wrap text-fg">{clip(typeof v === "string" ? v : JSON.stringify(v, null, 1), 600)}</dd>
        </div>
      ))}
    </dl>
  );
}

function EditDiff({ input }: { input: Input }) {
  const edits: Array<{ old_string?: unknown; new_string?: unknown }> = Array.isArray(input.edits) ? (input.edits as Input[]) : [{ old_string: input.old_string, new_string: input.new_string }];
  const diff = edits.map((e) => lineDiff(str(e.old_string), str(e.new_string))).join("\n@@\n");
  const st = diffStats(diff);
  return (
    <div>
      <div className="mb-1 flex items-center gap-2 text-[12.5px]">
        <FilePen className="size-3.5 text-muted" aria-hidden />
        <span className="min-w-0 truncate font-mono text-fg">{str(input.file_path)}</span>
        <span className="shrink-0 text-ok tabular">+{st.add}</span>
        <span className="shrink-0 text-danger tabular">−{st.del}</span>
      </div>
      <DiffView diff={diff} className="max-h-72" />
    </div>
  );
}

/** Spend payloads (browser checkout, shops): amount and merchant up front. */
function spendOf(input: Input): { amount: string; merchant: string } | null {
  const amt = input.amount ?? input.total ?? input.price ?? input.amount_eur ?? input.total_eur;
  const merchant = str(input.merchant ?? input.shop ?? input.store ?? input.site ?? input.vendor);
  if (amt == null || amt === "") return null;
  const cur = str(input.currency) || (typeof amt === "number" ? "EUR" : "");
  const amount = typeof amt === "number" ? new Intl.NumberFormat(undefined, { style: "currency", currency: cur || "EUR" }).format(amt) : str(amt);
  return { amount, merchant };
}

export function QuestionsPreview({ input, answers }: { input: Input; answers?: Record<string, string> }) {
  const qs = Array.isArray(input.questions) ? (input.questions as Input[]) : [];
  return (
    <div className="space-y-2.5">
      {qs.map((q, i) => (
        <div key={i}>
          {q.header ? <span className="mb-1 inline-block rounded-md bg-info-soft px-1.5 py-0.5 text-[11px] font-medium text-info">{str(q.header)}</span> : null}
          <div className="text-[13px] font-medium text-fg">{str(q.question)}</div>
          {answers?.[str(q.question)] ? (
            <div className="mt-1 text-[12.5px] text-fg-2">
              Answer: <span className="font-medium text-fg">{answers[str(q.question)]}</span>
            </div>
          ) : (
            <ul className="mt-1 flex flex-wrap gap-1.5">
              {(Array.isArray(q.options) ? (q.options as Input[]) : []).map((o, j) => (
                <li key={j} className="rounded-md border border-border px-2 py-0.5 text-[12px] text-fg-2" title={str(o.description)}>
                  {str(o.label)}
                </li>
              ))}
            </ul>
          )}
        </div>
      ))}
    </div>
  );
}

/** The readable body of a tool call (command, diff, URL, inputs…). */
export function ToolDetail({ tool, input = {}, cwd }: { tool: string; input?: Input; cwd?: string }) {
  const { name, server } = normalizeTool(tool);
  const spend = spendOf(input);
  if (spend) {
    return (
      <div className="flex items-center gap-3 rounded-lg border border-border bg-surface-2 p-3">
        <CreditCard className="size-5 shrink-0 text-warn" aria-hidden />
        <div className="min-w-0">
          <div className="text-[20px] leading-tight font-semibold text-fg tabular">{spend.amount}</div>
          {spend.merchant && <div className="truncate text-[13px] text-fg-2">at {spend.merchant}</div>}
        </div>
      </div>
    );
  }
  switch (name) {
    case "Bash":
    case "shell":
    case "shell.exec":
      return (
        <div>
          <Code>
            <span className="text-muted select-none">$ </span>
            {str(input.command ?? input.cmd)}
          </Code>
          {!!(cwd || input.cwd || input.description) && (
            <div className="mt-1 space-y-0.5 text-[12px] text-muted">
              {input.description ? <div>{str(input.description)}</div> : null}
              {cwd || input.cwd ? (
                <div>
                  in <span className="font-mono text-fg-2">{str(cwd || input.cwd)}</span>
                </div>
              ) : null}
            </div>
          )}
        </div>
      );
    case "Edit":
    case "MultiEdit":
      return <EditDiff input={input} />;
    case "Write": {
      const content = str(input.content);
      const lines = content.split("\n");
      return (
        <div>
          <div className="mb-1 flex items-center gap-2 text-[12.5px]">
            <FilePen className="size-3.5 text-muted" aria-hidden />
            <span className="min-w-0 truncate font-mono text-fg">{str(input.file_path)}</span>
            <span className="shrink-0 text-muted tabular">{lines.length} lines</span>
          </div>
          <Code>
            {lines.slice(0, 40).join("\n")}
            {lines.length > 40 ? "\n…" : ""}
          </Code>
        </div>
      );
    }
    case "WebFetch":
    case "web.fetch": {
      const url = str(input.url);
      return (
        <div className="space-y-1 text-[13px]">
          <a href={url} target="_blank" rel="noreferrer noopener" className="break-all text-accent underline-offset-2 hover:underline">
            {url}
          </a>
          {input.prompt ? <div className="text-[12.5px] text-fg-2">{str(input.prompt)}</div> : null}
        </div>
      );
    }
    case "WebSearch":
    case "web.search":
    case "web_search":
      return (
        <div className="flex items-center gap-2 text-[13px] text-fg">
          <Search className="size-3.5 text-muted" aria-hidden />“{str(input.query ?? input.q)}”
        </div>
      );
    case "AskUserQuestion":
      return <QuestionsPreview input={input} />;
  }
  return (
    <div>
      <div className="mb-1.5 font-mono text-[12px] text-fg-2">{server ? `${server} · ${name}` : friendlyToolName(tool)}</div>
      <KeyValues input={input} />
    </div>
  );
}

/** Small "Raw details" disclosure for the JSON behind a card. */
export function RawDetails({ value, label = "Raw details" }: { value: unknown; label?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="inline-flex min-h-7 items-center gap-1 rounded-md px-1 text-[11.5px] text-muted hover:text-fg focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:outline-none pointer-coarse:min-h-11"
      >
        <ChevronRight className={cx("size-3 transition-transform", open && "rotate-90")} aria-hidden />
        {label}
      </button>
      {open && <Code className="mt-1 text-fg-2">{JSON.stringify(value ?? {}, null, 2)}</Code>}
    </div>
  );
}

