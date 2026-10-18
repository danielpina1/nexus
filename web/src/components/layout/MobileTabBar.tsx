import { NavLink, useLocation, Link } from "react-router";
import { MoreHorizontal, Search, X } from "lucide-react";
import { SECTIONS, sectionFor } from "../../app/registry";
import { useUI } from "../../lib/ui";
import { cx } from "../../lib/format";
import { NavBadge } from "./NavBadge";
import { ConnectionIndicator } from "./ConnectionIndicator";
import { ThemeToggle } from "./ThemeToggle";

/** Bottom tab bar on phones: the primary sections plus "More" (a sheet with the rest). */
export function MobileTabBar() {
  const loc = useLocation();
  const active = sectionFor(loc.pathname).id;
  const set = useUI((s) => s.set);
  const navOpen = useUI((s) => s.navOpen);
  const tabs = SECTIONS.filter((s) => s.mobile === "tab");
  const more = SECTIONS.filter((s) => s.mobile === "more");
  const moreActive = more.some((s) => s.id === active);
  return (
    <>
      <nav
        aria-label="Main"
        className="fixed inset-x-0 bottom-0 z-20 border-t border-border bg-surface/95 pb-[env(safe-area-inset-bottom)] backdrop-blur-md lg:hidden"
      >
        <div className="mx-auto flex h-14 max-w-xl items-stretch">
          {tabs.map((s) => {
            const on = active === s.id;
            return (
              <NavLink key={s.id} to={s.to} end={s.to === "/"} className="relative flex flex-1 flex-col items-center justify-center gap-0.5" aria-current={on ? "page" : undefined}>
                <span className="relative">
                  <s.icon className={cx("size-[22px]", on ? "text-accent" : "text-muted")} strokeWidth={on ? 2.1 : 1.8} />
                  <NavBadge kind={s.badge} className="absolute -top-1.5 left-3.5 !min-w-4 !px-1 !text-[10px] !leading-4" />
                </span>
                <span className={cx("text-[10.5px] font-medium", on ? "text-fg" : "text-muted")}>{s.label}</span>
              </NavLink>
            );
          })}
          <button onClick={() => set({ navOpen: true })} className="flex flex-1 flex-col items-center justify-center gap-0.5" aria-label="More sections" aria-expanded={navOpen}>
            <MoreHorizontal className={cx("size-[22px]", moreActive ? "text-accent" : "text-muted")} />
            <span className={cx("text-[10.5px] font-medium", moreActive ? "text-fg" : "text-muted")}>More</span>
          </button>
        </div>
      </nav>
      {navOpen && <MoreSheet onClose={() => set({ navOpen: false })} active={active} />}
    </>
  );
}

function MoreSheet({ onClose, active }: { onClose: () => void; active: string }) {
  const openPalette = useUI((s) => s.openPalette);
  const more = SECTIONS.filter((s) => s.mobile === "more");
  return (
    <div className="fixed inset-0 z-30 lg:hidden" role="dialog" aria-modal="true" aria-label="More">
      <div className="absolute inset-0 bg-overlay" onClick={onClose} />
      <div className="animate-sheet-up absolute inset-x-0 bottom-0 rounded-t-2xl border-t border-border bg-surface px-3 pt-2 pb-[calc(env(safe-area-inset-bottom)+12px)] shadow-pop">
        <div className="mx-auto mb-2 h-1 w-9 rounded-full bg-border-strong" />
        <div className="flex items-center px-2 pb-1">
          <span className="flex-1 text-[13px] font-semibold text-muted">More</span>
          <button onClick={onClose} className="flex size-11 items-center justify-center rounded-lg text-muted hover:text-fg" aria-label="Close">
            <X className="size-5" />
          </button>
        </div>
        <button onClick={() => openPalette()} className="mb-1 flex h-12 w-full items-center gap-3 rounded-xl px-3 text-[15px] text-fg hover:bg-surface-2">
          <Search className="size-5 text-muted" /> Search or run…
        </button>
        {more.map((s) => (
          <Link key={s.id} to={s.to} onClick={onClose} className={cx("flex h-12 items-center gap-3 rounded-xl px-3 text-[15px]", active === s.id ? "bg-surface-2 text-fg" : "text-fg hover:bg-surface-2")}>
            <s.icon className={cx("size-5", active === s.id ? "text-accent" : "text-muted")} />
            {s.label}
          </Link>
        ))}
        <div className="mt-2 flex items-center justify-between border-t border-border px-1 pt-3">
          <ConnectionIndicator />
          <ThemeToggle />
        </div>
      </div>
    </div>
  );
}
