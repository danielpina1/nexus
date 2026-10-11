import type { ReactNode } from "react";
import { cx, type Tone } from "../../lib/format";

const tones: Record<Tone, string> = {
  neutral: "bg-surface-2 text-fg-2 border-border",
  accent: "bg-accent-soft text-accent border-transparent",
  ok: "bg-ok-soft text-ok border-transparent",
  warn: "bg-warn-soft text-warn border-transparent",
  danger: "bg-danger-soft text-danger border-transparent",
  info: "bg-info-soft text-info border-transparent",
};

export function Badge({
  tone = "neutral",
  children,
  className,
  dot,
  pulse,
  title,
}: {
  tone?: Tone;
  children: ReactNode;
  className?: string;
  dot?: boolean;
  pulse?: boolean;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 text-[11px] leading-none font-medium whitespace-nowrap",
        tones[tone],
        className,
      )}
    >
      {dot && <span className={cx("size-1.5 rounded-full bg-current", pulse && "animate-pulse-dot")} />}
      {children}
    </span>
  );
}
