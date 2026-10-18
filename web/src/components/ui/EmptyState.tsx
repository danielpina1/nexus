import type { ReactNode } from "react";
import { cx } from "../../lib/format";

export function EmptyState({
  icon,
  title,
  body,
  action,
  className,
  compact,
}: {
  icon?: ReactNode;
  title: ReactNode;
  body?: ReactNode;
  action?: ReactNode;
  className?: string;
  compact?: boolean;
}) {
  return (
    <div className={cx("flex flex-col items-center justify-center text-center", compact ? "px-4 py-8" : "px-6 py-16", className)}>
      {icon && <div className="mb-3 flex size-9 items-center justify-center rounded-full bg-surface-2 text-muted [&_svg]:size-[18px]">{icon}</div>}
      <div className="text-sm font-medium text-fg">{title}</div>
      {body && <div className="mt-1 max-w-sm text-[13px] text-muted">{body}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
