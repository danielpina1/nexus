import { cx } from "../../lib/format";

/** Thin horizontal bar for a 0–100 metric; color shifts toward warn/danger. */
export function Meter({ value, className, label }: { value: number; className?: string; label?: string }) {
  const v = Math.max(0, Math.min(100, value));
  const color = v >= 90 ? "bg-danger" : v >= 75 ? "bg-warn" : "bg-accent";
  return (
    <div className={cx("h-1.5 w-full overflow-hidden rounded-full bg-surface-3", className)} role="meter" aria-valuenow={v} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className={cx("h-full rounded-full transition-[width] duration-700", color)} style={{ width: `${v}%` }} />
    </div>
  );
}
