import { NavLink } from "react-router";
import { Search } from "lucide-react";
import { SECTIONS, prefetchPath } from "../../app/registry";
import { useUI } from "../../lib/ui";
import { cx } from "../../lib/format";
import { useLocation } from "react-router";
import { sectionFor } from "../../app/registry";
import { ConnectionIndicator } from "./ConnectionIndicator";
import { ThemeToggle } from "./ThemeToggle";
import { Kbd } from "../ui/Input";
import { Logo } from "./Logo";
import { NavBadge } from "./NavBadge";
import { isMac } from "../../lib/platform";


export function Sidebar() {
  const openPalette = useUI((s) => s.openPalette);
  const loc = useLocation();
  const active = sectionFor(loc.pathname).id;
  return (
    <div className="flex h-full flex-col px-3 pt-4 pb-3">
      <div className="mb-4 px-2">
        <Logo />
      </div>
      <button
        onClick={() => openPalette()}
        className="mb-3 flex h-9 items-center gap-2 rounded-lg border border-border bg-surface px-2.5 text-[13px] text-muted transition-colors hover:border-border-strong hover:text-fg"
        data-testid="open-palette"
      >
        <Search className="size-4" />
        <span className="min-w-0 flex-1 truncate text-left">Search or run…</span>
        <Kbd>{isMac ? "⌘" : "Ctrl"}</Kbd>
        <Kbd>K</Kbd>
      </button>
      <nav className="flex flex-col gap-0.5" aria-label="Main">
        {SECTIONS.map((s) => (
          <NavLink
            key={s.id}
            to={s.to}
            end={s.to === "/"}
            onMouseEnter={() => prefetchPath(s.to)}
            onFocus={() => prefetchPath(s.to)}
            aria-current={active === s.id ? "page" : undefined}
            className={cx(
              "group flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[13.5px] font-medium transition-colors duration-150",
              active === s.id ? "bg-surface-3/70 text-fg" : "text-fg-2 hover:bg-surface-2 hover:text-fg",
            )}
          >
            <s.icon className={cx("size-[17px] shrink-0", active === s.id ? "text-accent" : "text-muted group-hover:text-fg-2")} strokeWidth={1.9} />
            <span className="flex-1">{s.label}</span>
            <NavBadge kind={s.badge} />
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
