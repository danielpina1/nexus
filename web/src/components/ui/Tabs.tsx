import type { ReactNode } from "react";
import { cx } from "../../lib/format";

export interface TabItem<T extends string> {
  id: T;
  label: ReactNode;
  count?: number;
}

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  className,
}: {
  items: TabItem<T>[];
  value: T;
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <div role="tablist" className={cx("flex items-center gap-1 overflow-x-auto scrollbar-thin", className)}>
      {items.map((it) => {
        const active = it.id === value;
        return (
          <button
            key={it.id}
            role="tab"
            aria-selected={active}
            onClick={() => onChange(it.id)}
            className={cx(
              "flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-3 text-[13px] font-medium transition-colors",
              active ? "bg-surface text-fg shadow-card ring-1 ring-border" : "text-muted hover:bg-surface-2 hover:text-fg",
            )}
          >
            {it.label}
            {it.count != null && it.count > 0 && (
              <span className={cx("rounded-full px-1.5 text-[11px] tabular", active ? "bg-accent-soft text-accent" : "bg-surface-3 text-fg-2")}>
                {it.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
