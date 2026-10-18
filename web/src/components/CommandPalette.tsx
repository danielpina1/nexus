import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import {
  Activity,
  AtSign,
  CornerDownLeft,
  FileText,
  FlaskConical,
  Keyboard,
  Loader2,
  LogOut,
  Moon,
  OctagonX,
  RefreshCw,
  Search,
  Sparkles,
  Sun,
  Wifi,
} from "lucide-react";
import type { ReactNode } from "react";
import { useUI } from "../lib/ui";
import { useChat } from "../lib/chat";
import { useAllTasks, useInbox } from "../lib/selectors";
import { useAgents } from "../lib/queries";
import { api, isMock } from "../lib/api";
import { useSettings } from "../lib/settings";
import { forceReconnect, resetEventCache, stopEventStream } from "../lib/events";
import { toast } from "../lib/toast";
import { cx } from "../lib/format";
import { useIsDark } from "../lib/theme";
import { prefetchPath } from "../app/registry";
import { JUMPS } from "./useGlobalShortcuts";
import { SECTIONS } from "../app/registry";
import { Dialog } from "./ui/Dialog";
import { Kbd } from "./ui/Input";
import { AgentIcon } from "./AgentIcon";
import { StateIcon } from "./StateBadge";

interface Cmd {
  id: string;
  group: string;
  label: ReactNode;
  /** Plain text used for matching. */
  text: string;
  hint?: ReactNode;
  icon: ReactNode;
  run: () => void;
  /** Path to prefetch when highlighted. */
  to?: string;
  /** Always shown while typing (not filtered). */
  pinned?: boolean;
  score?: number;
}

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

/** Cheap fuzzy score: word-prefix and substring hits beat scattered subsequences; 0 = no match. */
export function fuzzyScore(hay: string, needle: string): number {
  const h = hay.toLowerCase();
  const n = needle.toLowerCase().trim();
  if (!n) return 1;
  let total = 0;
  for (const term of n.split(/\s+/)) {
    const i = h.indexOf(term);
    if (i === 0) total += 10;
    else if (i > 0) total += /[\s/·:-]/.test(h[i - 1]) ? 8 : 5;
    else {
      // subsequence
      let j = 0;
      for (const c of h) if (c === term[j]) j++;
      if (j < term.length) return 0;
      total += 1;
    }
  }
  return total;
}

export function CommandPalette() {
  const open = useUI((s) => s.paletteOpen);
  const setUI = useUI((s) => s.set);
  const close = () => setUI({ paletteOpen: false });
  return (
    <Dialog open={open} onClose={close} bare className="max-w-[640px] overflow-hidden" label="Command palette">
      {open && <PaletteBody close={close} />}
    </Dialog>
  );
}

function PaletteBody({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const initial = useUI.getState().paletteQuery;
  const [q, setQ] = useState(initial);
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const tasks = useAllTasks();
  const inbox = useInbox();
  const { data: agents } = useAgents();
  const send = useChat((s) => s.send);
  const update = useSettings((s) => s.update);
  const dark = useIsDark();
  const text = q.trim();

  // "@agent prompt" picks an agent explicitly.
  const mention = /^@(\S*)\s*(.*)$/s.exec(text);
  const mentionAgent = mention ? (agents ?? []).find((a) => a.id.toLowerCase() === mention[1].toLowerCase()) : undefined;
  const searchText = mention ? "" : text;
  const dq = useDebounced(searchText, 120);
  const notes = useQuery({
    queryKey: ["palette-search", dq],
    enabled: dq.length >= 2,
    queryFn: async ({ signal }) => (await api().vault.search({ query: dq, limit: 6 }, { signal })).hits,
    staleTime: 30_000,
    placeholderData: (prev) => prev,
  });

  const go = (to: string) => () => {
    close();
    navigate(to);
  };
  const ask = (prompt: string, agentId?: string) => () => {
    close();
    navigate("/chat");
    void send(prompt, agentId ? { agentId } : undefined);
  };

  const cmds = useMemo<Cmd[]>(() => {
    const list: Cmd[] = [];
    if (mention) {
      if (mentionAgent && mention[2].trim()) {
        list.push({
          id: "run-mention",
          group: "Run",
          pinned: true,
          text: "",
          label: (
            <span>
              Run with <span className="font-semibold">{mentionAgent.name}</span>: <span className="text-fg-2">{mention[2]}</span>
            </span>
          ),
          icon: <AgentIcon agentId={mentionAgent.id} icon={mentionAgent.icon} size="sm" />,
          run: ask(mention[2].trim(), mentionAgent.id),
        });
      } else {
        for (const a of agents ?? []) {
          const s = fuzzyScore(`${a.id} ${a.name}`, mention[1]);
          if (!s) continue;
          list.push({
            id: `pick-${a.id}`,
            group: "Pick an agent",
            pinned: true,
            score: s,
            text: "",
            label: (
              <span>
                @{a.id} <span className="text-muted">· {a.name}</span>
              </span>
            ),
            hint: a.description ? <span className="hidden max-w-56 truncate sm:inline">{a.description}</span> : undefined,
            icon: <AgentIcon agentId={a.id} icon={a.icon} size="sm" />,
            run: () => setQ(`@${a.id} `),
          });
        }
      }
      return list;
    }
    if (text) {
      list.push({
        id: "ask",
        group: "Run",
        pinned: true,
        text: "",
        label: (
          <span>
            Ask Nexus: <span className="font-semibold">{text}</span>
          </span>
        ),
        hint: "auto-routed",
        icon: <Sparkles className="size-4 text-accent" />,
        run: ask(text),
      });
    }
    for (const n of inbox.slice(0, 20)) {
      const title = n.kind === "approval" ? n.approval.title : n.kind === "question" ? n.item.question.text : n.item.notice.title;
      list.push({
        id: `inbox-${n.id}`,
        group: "Needs you",
        text: `inbox ${n.kind} ${title}`,
        label: title,
        hint: n.kind,
        icon: <span className={cx("size-2 rounded-full", n.kind === "approval" ? "bg-warn" : n.kind === "question" ? "bg-info" : "bg-border-strong")} />,
        run: go("/inbox"),
        to: "/inbox",
      });
    }
    for (const j of JUMPS) {
      const s = SECTIONS.find((x) => x.to === j.to);
      const Icon = s?.icon;
      list.push({ id: `nav-${j.key}`, group: "Go to", text: `go ${j.label}`, label: j.label, icon: Icon ? <Icon className="size-4" /> : <CornerDownLeft className="size-4" />, hint: <Kbd>g {j.key}</Kbd>, run: go(j.to), to: j.to });
    }
    for (const t of tasks.slice(0, 100)) {
      list.push({
        id: `task-${t.id}`,
        group: "Tasks",
        text: `task ${t.title} ${t.prompt} ${t.agentId} ${t.id}`,
        label: t.title || t.prompt,
        hint: t.agentId,
        icon: <StateIcon state={t.state} className="size-4 text-muted" />,
        run: go(`/tasks/${t.id}`),
        to: `/tasks/${t.id}`,
      });
    }
    for (const a of agents ?? []) {
      list.push({
        id: `agent-${a.id}`,
        group: "Agents",
        text: `agent ${a.name} ${a.id} ${a.description}`,
        label: a.name,
        hint: a.runtime,
        icon: <AgentIcon agentId={a.id} icon={a.icon} size="sm" />,
        run: go(`/agents/${a.id}`),
        to: `/agents/${a.id}`,
      });
    }
    for (const h of notes.data ?? []) {
      if (!h.note) continue;
      list.push({
        id: `note-${h.note.id}`,
        group: "Knowledge",
        pinned: true,
        text: "",
        label: h.note.title || h.note.path,
        hint: <span className="hidden font-mono sm:inline">{h.note.path}</span>,
        icon: <FileText className="size-4" />,
        run: go(`/vault?note=${encodeURIComponent(h.note.id || h.note.path)}`),
        to: "/vault",
      });
    }
    const action = (id: string, label: string, keywords: string, icon: ReactNode, run: () => void, hint?: ReactNode): Cmd => ({ id, group: "Actions", text: `${label} ${keywords}`, label, icon, run, hint });
    list.push(
      action("theme", dark ? "Switch to light theme" : "Switch to dark theme", "theme dark light appearance", dark ? <Sun className="size-4" /> : <Moon className="size-4" />, () => {
        update({ theme: dark ? "light" : "dark" });
        close();
      }),
      action("reconnect", "Reconnect to the Hub", "stream live sync refresh", <Wifi className="size-4" />, () => {
        forceReconnect();
        close();
        toast.info("Reconnecting…");
      }),
      action("perf", "Toggle performance HUD", "fps perf debug", <Activity className="size-4" />, () => {
        useUI.getState().togglePerf();
        close();
      }, <Kbd>`</Kbd>),
      action("keys", "Keyboard shortcuts", "help keys", <Keyboard className="size-4" />, () => {
        close();
        useUI.getState().set({ shortcutsOpen: true });
      }, <Kbd>?</Kbd>),
      action("resync", "Reset event cache and resync", "cache replay", <RefreshCw className="size-4" />, () => {
        resetEventCache();
        close();
        toast.info("Replaying the event log");
      }),
      action("demo", isMock() ? "Leave demo mode" : "Open demo mode", "mock demo sample", <FlaskConical className="size-4" />, () => {
        update({ mock: !isMock() });
        location.reload();
      }),
      action("kill", "Kill switch: stop all agents…", "stop all kill emergency", <OctagonX className="size-4 text-danger" />, go("/security?kill=1")),
    );
    if (!isMock()) {
      list.push(
        action("signout", "Sign out of this Hub", "logout token disconnect", <LogOut className="size-4" />, () => {
          stopEventStream();
          update({ token: "" });
          close();
        }),
      );
    }
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text, agents, tasks, inbox, notes.data, dark, mentionAgent?.id]);

  const filtered = useMemo(() => {
    if (mention) return cmds.slice().sort((a, b) => (b.score ?? 0) - (a.score ?? 0));
    if (!text) {
      // Empty query: what needs you, then navigation, recent tasks and actions.
      return [
        ...cmds.filter((c) => c.group === "Needs you").slice(0, 3),
        ...cmds.filter((c) => c.group === "Go to" && SECTIONS.some((s) => `nav-${s.key}` === c.id)),
        ...cmds.filter((c) => c.group === "Tasks").slice(0, 4),
        ...cmds.filter((c) => c.group === "Actions").slice(0, 4),
      ];
    }
    const order = ["Run", "Needs you", "Go to", "Tasks", "Agents", "Knowledge", "Actions"];
    const scored = cmds
      .map((c) => ({ ...c, score: c.pinned ? 1 : fuzzyScore(c.text, text) }))
      .filter((c) => (c.score ?? 0) > 0);
    const out: Cmd[] = [];
    for (const g of order) {
      const items = scored.filter((c) => c.group === g).sort((a, b) => (b.score ?? 0) - (a.score ?? 0));
      out.push(...items.slice(0, g === "Tasks" ? 6 : g === "Knowledge" ? 6 : 5));
    }
    return out;
  }, [cmds, text, mention]);

  const selIdx = Math.min(sel, Math.max(0, filtered.length - 1));
  const current = filtered[selIdx];
  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${selIdx}"]`)?.scrollIntoView({ block: "nearest" });
    if (current?.to) prefetchPath(current.to);
  }, [selIdx, current?.to]);

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) {
      e.preventDefault();
      setSel(Math.min(filtered.length - 1, selIdx + 1));
    } else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) {
      e.preventDefault();
      setSel(Math.max(0, selIdx - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      current?.run();
    } else if (e.key === "Tab" && mention && current?.id.startsWith("pick-")) {
      e.preventDefault();
      current.run();
    }
  };

  let lastGroup = "";
  const searching = notes.isFetching && dq.length >= 2;
  return (
    <div onKeyDown={onKey}>
      <div className="flex items-center gap-3 border-b border-border px-4">
        {searching ? <Loader2 className="size-[18px] animate-spin text-muted" /> : mention ? <AtSign className="size-[18px] text-accent" /> : <Search className="size-[18px] text-muted" />}
        <input
          data-autofocus
          data-testid="palette-input"
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          placeholder="Ask Nexus, run @agent, search notes, jump anywhere…"
          aria-label="Command"
          aria-controls="palette-list"
          aria-activedescendant={current ? `pal-${current.id}` : undefined}
          role="combobox"
          aria-expanded="true"
          className="h-14 flex-1 bg-transparent text-base text-fg outline-none placeholder:text-muted sm:text-[15px]"
          autoComplete="off"
          spellCheck={false}
        />
        <Kbd className="hidden sm:inline-flex">esc</Kbd>
      </div>
      <div ref={listRef} id="palette-list" role="listbox" className="max-h-[min(60vh,460px)] overflow-y-auto p-2 scrollbar-thin">
        {filtered.length === 0 && <div className="px-3 py-10 text-center text-[13px] text-muted">No matches. Press Enter to ask Nexus.</div>}
        {filtered.map((c, i) => {
          const header = c.group !== lastGroup ? c.group : null;
          lastGroup = c.group;
          return (
            <div key={c.id}>
              {header && <div className="px-2.5 pt-3 pb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">{header}</div>}
              <button
                id={`pal-${c.id}`}
                role="option"
                aria-selected={i === selIdx}
                data-idx={i}
                onMouseMove={() => i !== selIdx && setSel(i)}
                onClick={c.run}
                className={cx(
                  "flex min-h-10 w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left text-[13.5px] pointer-coarse:min-h-11",
                  i === selIdx ? "bg-surface-2 text-fg" : "text-fg-2",
                )}
              >
                <span className="flex size-6 shrink-0 items-center justify-center text-muted">{c.icon}</span>
                <span className="min-w-0 flex-1 truncate">{c.label}</span>
                {c.hint && <span className="shrink-0 truncate text-xs text-muted">{c.hint}</span>}
                {i === selIdx && <CornerDownLeft className="size-3.5 shrink-0 text-muted" />}
              </button>
            </div>
          );
        })}
      </div>
      <div className="hidden items-center gap-4 border-t border-border px-4 py-2 text-[11.5px] text-muted sm:flex">
        <span className="flex items-center gap-1">
          <Kbd>↑</Kbd>
          <Kbd>↓</Kbd> navigate
        </span>
        <span className="flex items-center gap-1">
          <Kbd>↵</Kbd> run
        </span>
        <span className="flex items-center gap-1">
          <Kbd>@</Kbd> pick an agent
        </span>
      </div>
    </div>
  );
}
