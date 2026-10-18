import { NavLink, useLocation } from "react-router";
import { prefetchPath, tabsFor } from "../../app/registry";
import { cx } from "../../lib/format";

/** Sub-navigation for sections with several pages (Work: Tasks · Agents · Spend). */
export function SectionTabs() {
  const loc = useLocation();
  const tabs = tabsFor(loc.pathname);
  if (!tabs) return null;
  return (
    <nav aria-label="Section" className="-mx-1 mb-5 flex gap-1 overflow-x-auto border-b border-border px-1 scrollbar-thin">
      {tabs.map((t) => (
        <NavLink
          key={t.to}
          to={t.to}
          end
          onMouseEnter={() => prefetchPath(t.to)}
          className={({ isActive }) =>
            cx(
              "relative -mb-px flex h-10 shrink-0 items-center border-b-2 px-3 text-[13.5px] font-medium transition-colors duration-150 pointer-coarse:h-11",
              isActive ? "border-accent text-fg" : "border-transparent text-muted hover:text-fg",
            )
          }
        >
          {t.label}
        </NavLink>
      ))}
    </nav>
  );
}
