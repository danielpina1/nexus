import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { CornerDownLeft, FileText, ListChecks, Moon, OctagonX, Search, Sparkles, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { useUI } from "../lib/ui";
import { useChat } from "../lib/chat";
import { useAllTasks } from "../lib/selectors";
import { useAgents } from "../lib/queries";
import { api } from "../lib/api";
import { useSettings } from "../lib/settings";
import { cx } from "../lib/format";
import { NAV } from "./layout/nav";
import { Dialog } from "./ui/Dialog";
import { Kbd } from "./ui/Input";
import { AgentIcon } from "./AgentIcon";
import { StateIcon } from "./StateBadge";

interface Cmd {
  id: string;
  group: string;
  label: ReactNode;
  hint?: ReactNode;
  icon: ReactNode;
  keywords: string;
  run: () => void;
}

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

export function CommandPalette() {
  const open = useUI((s) => s.paletteOpen);
  const setUI = useUI((s) => s.set);
  const close = () => setUI({ paletteOpen: false });
  return (
    <Dialog open={open} onClose={close} bare className="max-w-xl overflow-hidden">
      {open && <PaletteBody close={close} />}
    </Dialog>
  );
}

function PaletteBody({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const tasks = useAllTasks();
  const { data: agents } = useAgents();
  const send = useChat((s) => s.send);
  const theme = useSettings((s) => s.theme);
  const update = useSettings((s) => s.update);
  const dq = useDebounced(q.trim(), 180);
  const notes = useQuery({
    queryKey: ["palette-search", dq],
    enabled: dq.length >= 2,
    queryFn: async () => (await api().vault.search({ query: dq, limit: 5 })).hits,
    staleTime: 30_000,
  });

  const go = (to: string) => () => {
    close();
    navigate(to);
  };

  const cmds = useMemo<Cmd[]>(() => {
    const text = q.trim();
    const list: Cmd[] = [];
    if (text) {
      list.push({
        id: "run",
        group: "Command",
        label: (
          <span>
            Ask Nexus: <span className="font-semibold">{text}</span>
          </span>
        ),
        hint: "routes to the right agent",
        icon: <Sparkles className="size-4 text-accent" />,
        keywords: "",
        run: () => {
          close();
          navigate("/chat");
          void send(text);
        },
      });
    }
    for (const n of NAV) {
      list.push({ id: `nav-${n.to}`, group: "Go to", label: n.label, icon: <n.icon className="size-4" />, hint: <Kbd>g {n.key}</Kbd>, keywords: `go ${n.label}`, run: go(n.to) });
    }
    for (const a of agents ?? []) {
      list.push({
        id: `agent-${a.id}`,
        group: "Agents",
        label: a.name,
        hint: a.runtime,
        icon: <AgentIcon agentId={a.id} icon={a.icon} size="sm" />,
        keywords: `agent ${a.name} ${a.id} ${a.description}`,
        run: go(`/agents/${a.id}`),
      });
      if (text) {
        list.push({
          id: `ask-${a.id}`,
          group: "Run with agent",
          label: (
            <span>
              {a.name}: <span className="text-muted">{text}</span>
            </span>
          ),
          icon: <AgentIcon agentId={a.id} icon={a.icon} size="sm" />,
          keywords: `${a.name} ${a.id}`,
          run: () => {
            close();
            navigate("/chat");
            void send(text, { agentId: a.id });
          },
        });
      }
    }
    for (const t of tasks.slice(0, 60)) {
      list.push({
        id: `task-${t.id}`,
        group: "Tasks",
        label: t.title || t.prompt,
        icon: <StateIcon state={t.state} className="size-4 text-muted" />,
        keywords: `task ${t.title} ${t.agentId} ${t.id}`,
        run: go(`/tasks/${t.id}`),
      });
    }
    for (const h of notes.data ?? []) {
      if (!h.note) continue;
      list.push({
        id: `note-${h.note.id}`,
        group: "Vault",
        label: h.note.title || h.note.path,
        hint: h.note.path,
        icon: <FileText className="size-4" />,
        keywords: "",
        run: go(`/vault?note=${encodeURIComponent(h.note.id || h.note.path)}`),
      });
    }
    list.push(
      { id: "theme", group: "Actions", label: theme === "dark" ? "Switch to light theme" : "Switch to dark theme", icon: theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />, keywords: "theme dark light toggle", run: () => { update({ theme: theme === "dark" ? "light" : "dark" }); close(); } },
      { id: "running", group: "Actions", label: "Show running tasks", icon: <ListChecks className="size-4" />, keywords: "running tasks active", run: go("/tasks?status=active") },
      { id: "kill", group: "Actions", label: "Kill switch — stop all agents…", icon: <OctagonX className="size-4 text-danger" />, keywords: "stop all kill switch emergency", run: go("/security?kill=1") },
    );
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, agents, tasks, notes.data, theme]);

  const filtered = useMemo(() => {
    const text = q.trim().toLowerCase();
    if (!text) return cmds.filter((c) => c.group !== "Tasks").concat(cmds.filter((c) => c.group === "Tasks").slice(0, 5));
    const terms = text.split(/\s+/);
    return cmds.filter((c) => {
      if (c.group === "Command" || c.group === "Vault" || c.group === "Run with agent") return true;
      const hay = `${c.keywords} ${typeof c.label === "string" ? c.label : ""}`.toLowerCase();
      return terms.every((t) => hay.includes(t));
    }).filter((c, _i, arr) => c.group !== "Run with agent" || arr.filter((x) => x.group === "Run with agent").indexOf(c) < 3)
      .slice(0, 40);
  }, [cmds, q]);

  useEffect(() => {
    listRef.current?.querySelector(`[data-idx="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => Math.min(filtered.length - 1, s + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      filtered[sel]?.run();
    }
  };

  let lastGroup = "";
  return (
    <div onKeyDown={onKey}>
      <div className="flex items-center gap-2.5 border-b border-border px-4">
        <Search className="size-4 text-muted" />
        <input
          data-autofocus
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          placeholder="Type a command, ask Nexus, or search…"
          className="h-12 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-muted"
        />
        <Kbd>esc</Kbd>
      </div>
      <div ref={listRef} className="max-h-[min(60vh,440px)] overflow-y-auto p-2 scrollbar-thin">
        {filtered.length === 0 && <div className="px-3 py-8 text-center text-[13px] text-muted">No matches</div>}
        {filtered.map((c, i) => {
          const header = c.group !== lastGroup ? c.group : null;
          lastGroup = c.group;
          return (
            <div key={c.id}>
              {header && <div className="px-2.5 pt-2.5 pb-1 text-[11px] font-semibold tracking-wide text-muted uppercase">{header}</div>}
              <button
                data-idx={i}
                onMouseMove={() => setSel(i)}
                onClick={c.run}
                className={cx("flex w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left text-[13px]", i === sel ? "bg-accent-soft text-fg" : "text-fg-2")}
              >
                <span className="flex size-6 shrink-0 items-center justify-center text-muted">{c.icon}</span>
                <span className="min-w-0 flex-1 truncate">{c.label}</span>
                {c.hint && <span className="shrink-0 truncate text-xs text-muted">{c.hint}</span>}
                {i === sel && <CornerDownLeft className="size-3.5 shrink-0 text-muted" />}
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}
