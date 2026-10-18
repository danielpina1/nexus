import type { ReactNode } from "react";
import { cx } from "../../lib/format";

/**
 * A labelled meter for a value against a limit (tasks, runs, cost, time). Colour shifts to warn at
 * 75 % and danger at 90 %, and the numbers are always printed, so state never relies on colour alone.
 */
export function LimitMeter({
  label,
  icon,
  value,
  max,
  display,
  limitDisplay,
  hint,
  className,
}: {
  label: string;
  icon?: ReactNode;
  value: number;
  max?: number;
  display: ReactNode;
  limitDisplay?: ReactNode;
  hint?: ReactNode;
  className?: string;
}) {
  const pct = max && max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const tone = pct >= 90 ? "bg-danger" : pct >= 75 ? "bg-warn" : "bg-accent";
  const text = pct >= 90 ? "text-danger" : pct >= 75 ? "text-warn" : "text-fg";
  return (
    <div className={cx("rounded-xl border border-border bg-surface p-3.5 shadow-card", className)}>
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted">
        {icon && <span className="[&_svg]:size-3.5" aria-hidden>{icon}</span>}
        {label}
      </div>
      <div className="mt-1.5 flex items-baseline gap-1.5">
        <span className={cx("font-heading text-[20px] leading-7 font-semibold tabular", text)}>{display}</span>
        {limitDisplay != null && <span className="text-xs text-muted tabular">/ {limitDisplay}</span>}
      </div>
      <div
        className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-surface-3"
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={max || 100}
        aria-valuenow={max ? Math.min(value, max) : 0}
        aria-valuetext={max ? `${Math.round(pct)} % of the limit` : "no limit"}
      >
        {max ? <div className={cx("h-full rounded-full transition-[width] duration-700", tone)} style={{ width: `${pct}%` }} /> : null}
      </div>
      {hint && <div className="mt-1.5 truncate text-[11.5px] text-muted">{hint}</div>}
    </div>
  );
}
