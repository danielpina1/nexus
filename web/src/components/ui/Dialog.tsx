import { useEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";
import { cx } from "../../lib/format";

export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  className,
  bare,
  label,
}: {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  className?: string;
  /** No padding/header — used by the command palette. */
  bare?: boolean;
  /** Accessible name when there is no visible title. */
  label?: string;
}) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const prev = document.activeElement as HTMLElement | null;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey, true);
    const t = setTimeout(() => {
      const el = panel.current?.querySelector<HTMLElement>("[data-autofocus], input, textarea, button");
      el?.focus();
    }, 10);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      clearTimeout(t);
      prev?.focus?.();
    };
  }, [open, onClose]);

  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center p-3 pt-[max(12px,env(safe-area-inset-top))] sm:p-4 sm:pt-[12vh]">
      <div className="absolute inset-0 bg-overlay" onClick={onClose} />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={label ?? (typeof title === "string" ? title : undefined)}
        className={cx(
          "animate-pop-in relative w-full max-w-md rounded-2xl border border-border bg-surface shadow-pop",
          className,
        )}
      >
        {bare ? (
          children
        ) : (
          <>
            <div className="flex items-start gap-3 px-5 pt-5">
              <div className="min-w-0 flex-1">
                {title && <h2 className="text-[15px] font-semibold text-fg">{title}</h2>}
                {description && <p className="mt-1 text-[13px] text-muted">{description}</p>}
              </div>
              <button onClick={onClose} className="-mt-1.5 -mr-2 flex size-9 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:size-11" aria-label="Close">
                <X className="size-4" />
              </button>
            </div>
            <div className="px-5 py-4">{children}</div>
            {footer && <div className="flex justify-end gap-2 border-t border-border px-5 py-3">{footer}</div>}
          </>
        )}
      </div>
    </div>,
    document.body,
  );
}
