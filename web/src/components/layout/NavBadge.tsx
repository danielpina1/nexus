import { useInboxCount, useRunningCount } from "../../lib/selectors";
import { cx } from "../../lib/format";
import type { SectionDef } from "../../app/registry";

/** Count next to a nav item: inbox items (needs you) or running tasks. */
export function NavBadge({ kind, className }: { kind: SectionDef["badge"]; className?: string }) {
  const inbox = useInboxCount();
  const running = useRunningCount();
  if (!kind) return null;
  const n = kind === "inbox" ? inbox : running;
  if (n <= 0) return null;
  return (
    <span
      aria-label={kind === "inbox" ? `${n} need you` : `${n} running`}
      className={cx(
        "min-w-[18px] rounded-full px-1.5 text-center text-[11px] leading-[18px] font-semibold tabular",
        kind === "inbox" ? "bg-danger text-white dark:text-bg" : "bg-info-soft text-info",
        className,
      )}
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}
