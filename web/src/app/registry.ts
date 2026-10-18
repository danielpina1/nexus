import { Cpu, Home, Inbox, LibraryBig, MessageSquare, Settings, SquareStack } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ComponentType } from "react";

/**
 * The single registry of pages and navigation. Sidebar, mobile tab bar, section tabs, ⌘K
 * "Go to" commands, g-shortcuts, route definitions and hover-prefetch are all derived from it.
 *
 * Adding a feature (e.g. Teams) is one entry: a page in `PAGES`, plus a tab in its section:
 *
 *   teams: { path: "teams", load: () => import("../pages/Teams") },
 *   …and in SECTIONS.work.tabs: { to: "/teams", label: "Teams", page: "teams" }
 */

type Loader = () => Promise<{ default: ComponentType }>;

export interface PageDef {
  /** Route path relative to "/" ("" = index). */
  path: string;
  load: Loader;
  /** Render edge-to-edge without the page padding (chat). */
  fullBleed?: boolean;
}

export const PAGES = {
  overview: { path: "", load: () => import("../pages/Overview") },
  inbox: { path: "inbox", load: () => import("../pages/Inbox") },
  chat: { path: "chat", load: () => import("../pages/Chat"), fullBleed: true },
  tasks: { path: "tasks", load: () => import("../pages/Tasks") },
  task: { path: "tasks/:id", load: () => import("../pages/TaskDetail") },
  agents: { path: "agents", load: () => import("../pages/Agents") },
  agent: { path: "agents/:id", load: () => import("../pages/AgentDetail") },
  costs: { path: "costs", load: () => import("../pages/Costs") },
  vault: { path: "vault", load: () => import("../pages/Vault") },
  machines: { path: "machines", load: () => import("../pages/Machines") },
  machine: { path: "machines/:id", load: () => import("../pages/MachineDetail") },
  settings: { path: "settings", load: () => import("../pages/Settings") },
  security: { path: "security", load: () => import("../pages/Security") },
  notFound: { path: "*", load: () => import("../pages/NotFound") },
} satisfies Record<string, PageDef>;

export type PageId = keyof typeof PAGES;

export interface SectionTab {
  to: string;
  label: string;
  page: PageId;
}

export interface SectionDef {
  id: string;
  label: string;
  icon: LucideIcon;
  /** Where the nav item goes. */
  to: string;
  /** `g <key>` shortcut. */
  key: string;
  /** Path prefixes owned by this section (for the active state). */
  match: string[];
  /** Sub-pages shown as tabs at the top of the section. */
  tabs?: SectionTab[];
  /** Badge shown next to the nav item. */
  badge?: "inbox" | "running";
  /** On phones: in the bottom tab bar, or under "More". */
  mobile: "tab" | "more";
}

export const SECTIONS: SectionDef[] = [
  { id: "home", label: "Home", icon: Home, to: "/", key: "h", match: [], mobile: "tab" },
  { id: "inbox", label: "Inbox", icon: Inbox, to: "/inbox", key: "i", match: ["/inbox"], badge: "inbox", mobile: "tab" },
  { id: "chat", label: "Chat", icon: MessageSquare, to: "/chat", key: "c", match: ["/chat"], mobile: "tab" },
  {
    id: "work",
    label: "Work",
    icon: SquareStack,
    to: "/tasks",
    key: "w",
    match: ["/tasks", "/agents", "/costs"],
    badge: "running",
    mobile: "tab",
    tabs: [
      { to: "/tasks", label: "Tasks", page: "tasks" },
      { to: "/agents", label: "Agents", page: "agents" },
      { to: "/costs", label: "Spend", page: "costs" },
    ],
  },
  { id: "knowledge", label: "Knowledge", icon: LibraryBig, to: "/vault", key: "k", match: ["/vault"], mobile: "more" },
  { id: "machines", label: "Machines", icon: Cpu, to: "/machines", key: "m", match: ["/machines"], mobile: "more" },
  {
    id: "settings",
    label: "Settings",
    icon: Settings,
    to: "/settings",
    key: "s",
    match: ["/settings", "/security"],
    mobile: "more",
    tabs: [
      { to: "/settings", label: "General", page: "settings" },
      { to: "/security", label: "Security", page: "security" },
    ],
  },
];

/** Extra `g <key>` jumps to sub-pages. */
export const SUB_KEYS: Array<{ key: string; to: string; label: string }> = [
  { key: "t", to: "/tasks", label: "Tasks" },
  { key: "a", to: "/agents", label: "Agents" },
  { key: "v", to: "/vault", label: "Vault" },
  { key: "$", to: "/costs", label: "Spend" },
  { key: "o", to: "/", label: "Home" },
  { key: ",", to: "/settings", label: "Settings" },
];

export function sectionFor(pathname: string): SectionDef {
  return SECTIONS.find((s) => s.match.some((m) => pathname === m || pathname.startsWith(m + "/"))) ?? SECTIONS[0];
}

/** Tabs to show for a path: only on a tab's own page (not on detail pages like /tasks/:id). */
export function tabsFor(pathname: string): SectionTab[] | null {
  const s = sectionFor(pathname);
  if (!s.tabs || s.tabs.length < 2) return null;
  return s.tabs.some((t) => t.to === pathname) ? s.tabs : null;
}

export function isFullBleed(pathname: string): boolean {
  return pathname === "/chat" || pathname.startsWith("/chat/");
}

const prefetched = new Set<PageId>();

/** Warm the code-split chunk for a path (on hover / focus / idle). */
export function prefetchPath(to: string) {
  const path = to.split(/[?#]/)[0].replace(/^\//, "");
  const seg = path.split("/");
  let id: PageId | undefined;
  for (const [k, p] of Object.entries(PAGES) as Array<[PageId, PageDef]>) {
    const ps = p.path.split("/");
    if (p.path === "*") continue;
    if (ps.length === seg.length && ps.every((x, i) => x.startsWith(":") || x === seg[i])) {
      id = k;
      break;
    }
  }
  if (!id || prefetched.has(id)) return;
  prefetched.add(id);
  void PAGES[id].load().catch(() => prefetched.delete(id!));
}
