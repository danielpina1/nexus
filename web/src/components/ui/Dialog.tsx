import type { ReactNode } from "react";
import * as D from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cx } from "../../lib/format";

/**
 * Modal dialog (shadcn/ui-style, on Radix Dialog): focus trap, Escape, scroll lock, aria wiring and
 * focus return come from Radix. Glass panel over a dim scrim.
 */
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
  return (
    <D.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <D.Portal>
        <D.Overlay className="animate-fade-in fixed inset-0 z-50 bg-overlay" />
        <div className="pointer-events-none fixed inset-0 z-50 flex items-start justify-center p-3 pt-[max(12px,env(safe-area-inset-top))] sm:p-4 sm:pt-[12vh]">
          <D.Content
            {...(description ? {} : { "aria-describedby": undefined })}
            onOpenAutoFocus={(e) => {
              // Prefer an explicit [data-autofocus] target (palette input) over the first button.
              const el = (e.currentTarget as HTMLElement | null)?.querySelector<HTMLElement>("[data-autofocus]");
              if (el) {
                e.preventDefault();
                el.focus();
              }
            }}
            className={cx("glass animate-pop-in pointer-events-auto relative w-full max-w-md rounded-2xl border shadow-pop outline-none", className)}
          >
            {bare ? (
              <>
                <D.Title className="sr-only">{label ?? "Dialog"}</D.Title>
                {children}
              </>
            ) : (
              <>
                <div className="flex items-start gap-3 px-5 pt-5">
                  <div className="min-w-0 flex-1">
                    {title ? <D.Title className="text-[16px] font-semibold text-fg">{title}</D.Title> : <D.Title className="sr-only">{label ?? "Dialog"}</D.Title>}
                    {description && <D.Description className="mt-1 text-[13px] text-muted">{description}</D.Description>}
                  </div>
                  <D.Close className="-mt-1.5 -mr-2 flex size-9 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg pointer-coarse:size-11" aria-label="Close">
                    <X className="size-4" />
                  </D.Close>
                </div>
                <div className="px-5 py-4">{children}</div>
                {footer && <div className="flex justify-end gap-2 border-t border-border px-5 py-3">{footer}</div>}
              </>
            )}
          </D.Content>
        </div>
      </D.Portal>
    </D.Root>
  );
}
