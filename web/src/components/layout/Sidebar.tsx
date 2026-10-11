import { NavLink } from "react-router";
import { Search } from "lucide-react";
import { NAV } from "./nav";
import { useInboxCount, useRunningTasks } from "../../lib/selectors";
import { useUI } from "../../lib/ui";
import { cx } from "../../lib/format";
import { ConnectionIndicator } from "./ConnectionIndicator";
import { ThemeToggle } from "./ThemeToggle";
import { Kbd } from "../ui/Input";
import { isMock } from "../../lib/api";

export function Logo() {
  return (
    <div className="flex items-center gap-2">
      <div className="flex size-7 items-center justify-center rounded-lg bg-accent text-accent-fg shadow-card">
        <svg viewBox="0 0 32 32" className="size-4" fill="none" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M9 23V9l14 14V9" />
        </svg>
      </div>
      <span className="text-[15px] font-semibold tracking-tight text-fg">Nexus</span>
      {isMock() && <span className="rounded bg-warn-soft px-1.5 py-0.5 text-[10px] font-semibold tracking-wide text-warn uppercase">mock</span>}
    </div>
  );
}

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const inbox = useInboxCount();
  const running = useRunningTasks().length;
  const openPalette = () => useUI.getState().set({ paletteOpen: true });
  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  return (
    <div className="flex h-full flex-col gap-1 px-3 py-4">
      <div className="mb-3 px-2">
        <Logo />
      </div>
      <button
        onClick={() => {
          onNavigate?.();
          openPalette();
        }}
        className="mb-2 flex h-9 items-center gap-2 rounded-lg border border-border bg-surface px-2.5 text-[13px] text-muted shadow-card transition-colors hover:border-border-strong hover:text-fg"
      >
        <Search className="size-4" />
        <span className="flex-1 text-left">Search or command…</span>
        <Kbd>{isMac ? "⌘" : "Ctrl"}</Kbd>
        <Kbd>K</Kbd>
      </button>
      <nav className="flex flex-col gap-0.5">
        {NAV.map(({ to, label, icon: Icon }) => (
          <NavLink
            key={to}
            to={to}
            end={to === "/"}
            onClick={onNavigate}
            className={({ isActive }) =>
              cx(
                "group flex h-8 items-center gap-2.5 rounded-lg px-2.5 text-[13px] font-medium transition-colors",
                isActive ? "bg-surface text-fg shadow-card ring-1 ring-border" : "text-fg-2 hover:bg-surface-2 hover:text-fg",
              )
            }
          >
            <Icon className="size-4 shrink-0 opacity-80" />
            <span className="flex-1">{label}</span>
            {to === "/inbox" && inbox > 0 && (
              <span className="min-w-5 rounded-full bg-danger px-1.5 text-center text-[11px] leading-5 font-semibold text-white tabular">{inbox}</span>
            )}
            {to === "/tasks" && running > 0 && (
              <span className="min-w-5 rounded-full bg-info-soft px-1.5 text-center text-[11px] leading-5 font-semibold text-info tabular">{running}</span>
            )}
          </NavLink>
        ))}
      </nav>
      <div className="flex-1" />
      <div className="flex items-center justify-between gap-2 border-t border-border pt-3">
        <div className="min-w-0 flex-1">
          <ConnectionIndicator />
        </div>
        <ThemeToggle />
      </div>
    </div>
  );
}
