import { CheckCircle2, Info, TriangleAlert, X, XCircle } from "lucide-react";
import { useToasts } from "../lib/toast";
import { cx } from "../lib/format";

export function Toaster() {
  const toasts = useToasts((s) => s.toasts);
  const remove = useToasts((s) => s.remove);
  return (
    <div className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2" aria-live="polite">
      {toasts.map((t) => {
        const Icon = t.tone === "success" ? CheckCircle2 : t.tone === "error" ? XCircle : t.tone === "warn" ? TriangleAlert : Info;
        return (
          <div key={t.id} className="animate-toast-in pointer-events-auto flex items-start gap-3 rounded-xl border border-border bg-surface p-3 shadow-pop">
            <Icon className={cx("mt-0.5 size-4 shrink-0", t.tone === "success" ? "text-ok" : t.tone === "error" ? "text-danger" : t.tone === "warn" ? "text-warn" : "text-info")} />
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium text-fg">{t.title}</div>
              {t.body && <div className="mt-0.5 text-xs break-words text-muted">{t.body}</div>}
            </div>
            <button onClick={() => remove(t.id)} className="text-muted hover:text-fg" aria-label="Dismiss">
              <X className="size-3.5" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
