import { Suspense, useEffect } from "react";
import { Link, Outlet, useLocation } from "react-router";
import { Inbox, Menu, Search, X } from "lucide-react";
import { Sidebar, Logo } from "./Sidebar";
import { CommandPalette } from "../CommandPalette";
import { Toaster } from "../Toaster";
import { ShortcutsDialog, useGlobalShortcuts } from "../Shortcuts";
import { useUI } from "../../lib/ui";
import { useAgents, useNodes, usePendingApprovalsSeed, useTaskList } from "../../lib/queries";
import { useInboxCount } from "../../lib/selectors";
import { ConnectionIndicator } from "./ConnectionIndicator";
import { cx } from "../../lib/format";
import { Skeleton } from "../ui/Skeleton";

function PageFallback() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-8 w-48" />
      <div className="grid gap-4 md:grid-cols-3">
        <Skeleton className="h-32" />
        <Skeleton className="h-32" />
        <Skeleton className="h-32" />
      </div>
      <Skeleton className="h-64" />
    </div>
  );
}

export function AppShell() {
  useGlobalShortcuts();
  // Seed the store so pages render before (or without) a full event replay.
  usePendingApprovalsSeed();
  useNodes();
  useTaskList({});
  useAgents();

  const navOpen = useUI((s) => s.navOpen);
  const set = useUI((s) => s.set);
  const inbox = useInboxCount();
  const loc = useLocation();
  useEffect(() => set({ navOpen: false }), [loc.pathname, set]);
  const fullBleed = loc.pathname.startsWith("/chat");

  return (
    <div className="flex h-dvh overflow-hidden bg-bg">
      <aside className="hidden w-60 shrink-0 border-r border-border bg-bg lg:block">
        <Sidebar />
      </aside>

      {/* mobile drawer */}
      <div className={cx("fixed inset-0 z-40 lg:hidden", navOpen ? "" : "pointer-events-none")}>
        <div className={cx("absolute inset-0 bg-black/40 transition-opacity", navOpen ? "opacity-100" : "opacity-0")} onClick={() => set({ navOpen: false })} />
        <div className={cx("absolute inset-y-0 left-0 w-72 border-r border-border bg-bg shadow-pop transition-transform", navOpen ? "translate-x-0" : "-translate-x-full")}>
          <button className="absolute top-4 right-3 rounded-md p-1 text-muted hover:text-fg" onClick={() => set({ navOpen: false })} aria-label="Close menu">
            <X className="size-5" />
          </button>
          <Sidebar onNavigate={() => set({ navOpen: false })} />
        </div>
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-bg/90 px-3 backdrop-blur lg:hidden">
          <button className="rounded-lg p-2 text-fg-2 hover:bg-surface-2" onClick={() => set({ navOpen: true })} aria-label="Open menu">
            <Menu className="size-5" />
          </button>
          <Logo />
          <div className="flex-1" />
          <ConnectionIndicator compact />
          <button className="rounded-lg p-2 text-fg-2 hover:bg-surface-2" onClick={() => set({ paletteOpen: true })} aria-label="Search">
            <Search className="size-5" />
          </button>
          <Link to="/inbox" className="relative rounded-lg p-2 text-fg-2 hover:bg-surface-2" aria-label="Inbox">
            <Inbox className="size-5" />
            {inbox > 0 && <span className="absolute top-1 right-1 min-w-4 rounded-full bg-danger px-1 text-center text-[10px] leading-4 font-semibold text-white">{inbox}</span>}
          </Link>
        </header>
        <main className={cx("min-h-0 flex-1", fullBleed ? "overflow-hidden" : "overflow-y-auto scrollbar-thin")}>
          <div className={cx("mx-auto w-full", fullBleed ? "h-full" : "max-w-7xl px-4 py-5 sm:px-6 lg:px-8 lg:py-7")}>
            <Suspense fallback={<div className="p-6"><PageFallback /></div>}>
              <Outlet />
            </Suspense>
          </div>
        </main>
      </div>

      <CommandPalette />
      <ShortcutsDialog />
      <Toaster />
    </div>
  );
}
