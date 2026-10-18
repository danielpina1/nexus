import { lazy, Suspense, useEffect } from "react";
import { Outlet, useLocation } from "react-router";
import { Search } from "lucide-react";
import { Sidebar } from "./Sidebar";
import { MobileTabBar } from "./MobileTabBar";
import { SectionTabs } from "./SectionTabs";
import { Logo } from "./Logo";
import { Toaster } from "../Toaster";
import { useGlobalShortcuts } from "../useGlobalShortcuts";
import { useUI } from "../../lib/ui";
import { useAgents, useNodes, usePendingApprovalsSeed, useTaskList } from "../../lib/queries";
import { ConnectionIndicator } from "./ConnectionIndicator";
import { OfflineBanner } from "./OfflineBanner";
import { cx } from "../../lib/format";
import { Skeleton } from "../ui/Skeleton";
import { isFullBleed, prefetchPath, SECTIONS } from "../../app/registry";

const PerfHUD = lazy(() => import("../PerfHUD"));
const loadPalette = () => import("../CommandPalette");
const CommandPalette = lazy(() => loadPalette().then((m) => ({ default: m.CommandPalette })));
const ShortcutsDialog = lazy(() => import("../Shortcuts").then((m) => ({ default: m.ShortcutsDialog })));

function PageFallback() {
  return (
    <div className="space-y-4" aria-busy="true" aria-label="Loading">
      <Skeleton className="h-7 w-44" />
      <Skeleton className="h-4 w-72" />
      <Skeleton className="mt-6 h-40" />
      <Skeleton className="h-24" />
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

  const set = useUI((s) => s.set);
  const openPalette = useUI((s) => s.openPalette);
  const perfOpen = useUI((s) => s.perfOpen);
  const paletteOpen = useUI((s) => s.paletteOpen);
  const shortcutsOpen = useUI((s) => s.shortcutsOpen);
  const loc = useLocation();
  useEffect(() => set({ navOpen: false }), [loc.pathname, set]);
  const fullBleed = isFullBleed(loc.pathname);

  // Warm the primary sections' chunks once the app is idle.
  useEffect(() => {
    const idle = (window as { requestIdleCallback?: (cb: () => void) => number }).requestIdleCallback ?? ((cb: () => void) => setTimeout(cb, 1500));
    idle(() => {
      for (const s of SECTIONS) prefetchPath(s.to);
      void loadPalette();
    });
  }, []);

  return (
    <div className="flex h-dvh overflow-hidden bg-bg">
      <a href="#main" className="sr-only z-[80] rounded-lg bg-primary px-3 py-2 text-primary-fg focus:not-sr-only focus:fixed focus:top-3 focus:left-3">
        Skip to content
      </a>
      <aside className="glass hidden w-[232px] shrink-0 border-r lg:block">
        <Sidebar />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="glass sticky top-0 z-20 flex h-12 shrink-0 items-center gap-1 border-b px-3 pt-[env(safe-area-inset-top)] lg:hidden">
          <Logo />
          <div className="flex-1" />
          <ConnectionIndicator compact />
          <button className="flex size-11 items-center justify-center rounded-lg text-fg-2 hover:bg-surface-2" onClick={() => openPalette()} aria-label="Search or run a command">
            <Search className="size-5" />
          </button>
        </header>
        <OfflineBanner />
        <main
          id="main"
          tabIndex={-1}
          className={cx(
            "min-h-0 flex-1",
            fullBleed ? "overflow-hidden pb-[calc(56px+env(safe-area-inset-bottom))] lg:pb-0" : "overflow-y-auto scrollbar-thin",
          )}
        >
          <div
            className={cx(
              "mx-auto w-full",
              fullBleed ? "h-full" : "max-w-[1200px] px-4 pt-5 pb-[calc(80px+env(safe-area-inset-bottom))] sm:px-6 lg:px-10 lg:pt-8 lg:pb-12",
            )}
          >
            {!fullBleed && <SectionTabs />}
            <Suspense fallback={<PageFallback />}>
              <div key={loc.pathname} className={fullBleed ? "h-full" : "animate-fade-up"}>
                <Outlet />
              </div>
            </Suspense>
          </div>
        </main>
      </div>

      <MobileTabBar />
      {paletteOpen && (
        <Suspense fallback={null}>
          <CommandPalette />
        </Suspense>
      )}
      {shortcutsOpen && (
        <Suspense fallback={null}>
          <ShortcutsDialog />
        </Suspense>
      )}
      <Toaster />
      {perfOpen && (
        <Suspense fallback={null}>
          <PerfHUD />
        </Suspense>
      )}
    </div>
  );
}
