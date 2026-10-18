import type { ReactNode } from "react";
import * as T from "@radix-ui/react-tabs";
import { cx } from "../../lib/format";

export interface TabItem<V extends string> {
  id: V;
  label: ReactNode;
  count?: number;
}

/**
 * Segmented tabs (shadcn/ui-style on Radix Tabs): arrow keys move between tabs, Home/End jump,
 * selection follows focus. Content is rendered by the caller from `value`.
 */
export function Tabs<V extends string>({
  items,
  value,
  onChange,
  className,
  label,
}: {
  items: TabItem<V>[];
  value: V;
  onChange: (v: V) => void;
  className?: string;
  label?: string;
}) {
  return (
    <T.Root value={value} onValueChange={(v) => onChange(v as V)} activationMode="automatic">
      <T.List aria-label={label} className={cx("flex items-center gap-1 overflow-x-auto scrollbar-thin", className)}>
        {items.map((it) => {
          const active = it.id === value;
          return (
            <T.Trigger
              key={it.id}
              value={it.id}
              className={cx(
                "flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-3 text-[13px] font-medium transition-colors duration-150 pointer-coarse:h-10",
                active ? "bg-surface-3/70 text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
              )}
            >
              {it.label}
              {it.count != null && it.count > 0 && (
                <span className={cx("rounded-full px-1.5 text-[11px] tabular", active ? "bg-surface text-fg" : "bg-surface-3 text-fg-2")}>{it.count}</span>
              )}
            </T.Trigger>
          );
        })}
      </T.List>
    </T.Root>
  );
}
