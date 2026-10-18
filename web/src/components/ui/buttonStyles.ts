import { cx } from "../../lib/format";

export type Variant = "primary" | "secondary" | "ghost" | "danger" | "outline" | "success";
export type Size = "xs" | "sm" | "md" | "icon";

export const variants: Record<Variant, string> = {
  primary: "bg-primary text-primary-fg hover:opacity-90",
  secondary: "bg-surface text-fg hover:bg-surface-2 border border-border shadow-card",
  outline: "border border-border-strong text-fg hover:bg-surface-2",
  ghost: "text-fg-2 hover:bg-surface-2 hover:text-fg",
  danger: "bg-danger text-white hover:opacity-90 dark:text-bg",
  success: "bg-ok text-white hover:opacity-90 dark:text-bg",
};

export const sizes: Record<Size, string> = {
  // Touch screens get ≥ 44 px targets (UI UX Pro Max: touch-target-size).
  xs: "h-7 px-2 text-xs gap-1 rounded-md pointer-coarse:h-10 pointer-coarse:px-3",
  sm: "h-8 px-3 text-[13px] gap-1.5 rounded-lg pointer-coarse:h-11",
  md: "h-9 px-4 text-sm gap-2 rounded-lg pointer-coarse:h-11",
  icon: "h-8 w-8 rounded-lg justify-center pointer-coarse:size-11",
};

/** The same look for a navigation link (react-router `<Link className={buttonClass(…)}>`). */
export function buttonClass(variant: Variant = "secondary", size: Size = "sm", className?: string): string {
  return cx(
    "inline-flex shrink-0 items-center font-medium whitespace-nowrap transition-[background,opacity,color,transform] duration-100 select-none active:scale-[0.98]",
    variants[variant],
    sizes[size],
    className,
  );
}

