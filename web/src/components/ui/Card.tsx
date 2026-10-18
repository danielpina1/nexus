import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../../lib/format";

export function Card({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cx("rounded-xl border border-border bg-surface shadow-card", className)} {...rest} />;
}

export function CardHeader({
  title,
  subtitle,
  icon,
  actions,
  className,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  icon?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("flex min-h-12 items-center gap-3 border-b border-border px-4 py-2.5", className)}>
      {icon && <div className="text-muted">{icon}</div>}
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-semibold text-fg">{title}</div>
        {subtitle && <div className="truncate text-xs text-muted">{subtitle}</div>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  );
}
