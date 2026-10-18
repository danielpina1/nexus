import type { LucideIcon } from "lucide-react";
import { BookOpen, Bot, Compass, FilePen, FileText, Globe, ListChecks, MessageCircleQuestion, Search, SquareTerminal, Users, Wrench } from "lucide-react";

/**
 * Human-readable names of tool calls, shared by the chat chips, the run timeline and the approval
 * cards (components/ToolView.tsx renders the details).
 */

type Input = Record<string, unknown>;

const NEXUS_NS = ["memory", "vault", "web", "shell", "fs", "team", "workflow", "browser", "nexus", "shops", "facts", "architect", "github", "email", "git", "anthrex", "router", "tools"];

export interface NormalTool {
  /** Canonical name: Nexus dotted name, or the client's own tool name. */
  name: string;
  /** MCP server for foreign MCP tools ("github"), "" otherwise. */
  server: string;
}

export function normalizeTool(tool: string): NormalTool {
  let t = tool;
  if (t.startsWith("mcp__")) {
    const [server, ...rest] = t.slice(5).split("__");
    const name = rest.join("__");
    if (server !== "nexus") return { name, server };
    t = name;
  }
  if (t.startsWith("nexus.") && NEXUS_NS.includes(t.split(".")[1])) t = t.slice(6);
  if (!t.includes(".")) {
    const i = t.indexOf("_");
    if (i > 0 && NEXUS_NS.includes(t.slice(0, i))) t = t.slice(0, i) + "." + t.slice(i + 1);
  }
  return { name: t, server: "" };
}

export const str = (v: unknown): string => (typeof v === "string" ? v : v == null ? "" : JSON.stringify(v));
export const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + "…" : s);
const base = (p: string) => p.split("/").filter(Boolean).pop() ?? p;
const host = (u: string) => {
  try {
    return new URL(u).host;
  } catch {
    return u;
  }
};

export interface ToolTitle {
  title: string;
  icon: LucideIcon;
}

/** A one-line, past-tense human summary of a tool call ("Searched the vault for “x”"). */
export function toolTitle(tool: string, input: Input = {}): ToolTitle {
  const { name, server } = normalizeTool(tool);
  const path = str(input.file_path ?? input.path ?? input.note ?? input.notebook_path);
  const q = str(input.query ?? input.q ?? input.pattern);
  switch (name) {
    case "Bash":
    case "shell":
    case "shell.exec":
      return { title: `Ran ${clip(str(input.command ?? input.cmd), 70) || "a command"}`, icon: SquareTerminal };
    case "Read":
    case "fs.read":
    case "NotebookRead":
      return { title: `Read file ${base(path)}`, icon: FileText };
    case "Edit":
    case "MultiEdit":
    case "NotebookEdit":
    case "apply_patch":
      return { title: path ? `Edited ${base(path)}` : "Edited files", icon: FilePen };
    case "Write":
      return { title: `Wrote ${base(path)}`, icon: FilePen };
    case "Grep":
    case "Glob":
    case "LS":
      return { title: q ? `Searched files for “${clip(q, 50)}”` : `Listed ${base(path) || "files"}`, icon: Search };
    case "WebFetch":
    case "web.fetch":
      return { title: `Fetched ${host(str(input.url))}`, icon: Globe };
    case "WebSearch":
    case "web_search":
    case "web.search":
      return { title: `Searched the web for “${clip(q, 60)}”`, icon: Globe };
    case "memory.search":
      return { title: q ? `Searched the vault for “${clip(q, 60)}”` : "Searched the vault", icon: BookOpen };
    case "vault.read":
      return { title: `Read note ${base(path) || ""}`.trim(), icon: BookOpen };
    case "vault.write":
    case "vault.daily_append":
      return { title: `Wrote note ${base(path) || ""}`.trim(), icon: BookOpen };
    case "vault.query":
    case "vault.related":
    case "vault.tasks":
      return { title: "Looked through the vault", icon: BookOpen };
    case "spawn_agent":
      return { title: `Delegated to ${str(input.agent_id) || "an agent"}`, icon: Bot };
    case "team.delegate":
    case "team.delegate_many":
    case "team.spawn":
      return { title: `Delegated to ${str(input.role ?? input.agent) || "team members"}`, icon: Users };
    case "team.await":
      return { title: "Waited for team results", icon: Users };
    case "ask_user":
    case "AskUserQuestion":
      return { title: "Asked you a question", icon: MessageCircleQuestion };
    case "nexus.status":
      return { title: "Checked Nexus status", icon: Compass };
    case "workflow.list":
      return { title: "Listed workflows", icon: ListChecks };
    case "workflow.run":
      return { title: `Started workflow ${str(input.workflow_id ?? input.id)}`, icon: ListChecks };
    case "TodoWrite":
      return { title: "Updated its plan", icon: ListChecks };
    case "notify":
      return { title: "Sent you a notification", icon: MessageCircleQuestion };
  }
  if (name.startsWith("browser.")) return { title: `Browser · ${name.slice(8).replace(/_/g, " ")}`, icon: Globe };
  if (server) return { title: `Used ${server} · ${name}`, icon: Wrench };
  return { title: `Used ${name}`, icon: Wrench };
}

/** "nexus · vault.write" / "github · create_issue" / "Bash". */
export function friendlyToolName(tool: string): string {
  const { name, server } = normalizeTool(tool);
  if (server) return `${server} · ${name}`;
  if (tool.startsWith("mcp__nexus__") || tool.includes(".")) return `nexus · ${name}`;
  return name;
}

// ---------------------------------------------------------------- diffs

/** Minimal line diff (LCS) as a unified diff body. Inputs are small (tool edits). */
export function lineDiff(oldText: string, newText: string): string {
  const a = oldText ? oldText.replace(/\n$/, "").split("\n") : [];
  const b = newText ? newText.replace(/\n$/, "").split("\n") : [];
  if (a.length * b.length > 250_000) return [...a.map((l) => "-" + l), ...b.map((l) => "+" + l)].join("\n");
  const dp: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) for (let j = b.length - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: string[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push(" " + a[i]);
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) out.push("-" + a[i++]);
    else out.push("+" + b[j++]);
  }
  while (i < a.length) out.push("-" + a[i++]);
  while (j < b.length) out.push("+" + b[j++]);
  return out.join("\n");
}

export function diffStats(diff: string): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const l of diff.split("\n")) {
    if (l.startsWith("+")) add++;
    else if (l.startsWith("-")) del++;
  }
  return { add, del };
}

