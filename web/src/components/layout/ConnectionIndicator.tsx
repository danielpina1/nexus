import { useEvents } from "../../lib/store";
import { reconnectNow } from "../../lib/events";
import { useNow } from "../../lib/useNow";
import { cx } from "../../lib/format";
import { isMock } from "../../lib/api";

export function ConnectionIndicator({ compact }: { compact?: boolean }) {
  const c = useEvents((s) => s.connection);
  const now = useNow(1000);
  const mock = isMock();
  let color = "bg-muted";
  let label = "Idle";
  switch (c.status) {
    case "live":
      color = "bg-ok";
      label = mock ? "Live · demo" : "Live";
      break;
    case "connecting":
      color = "bg-warn";
      label = "Connecting…";
      break;
    case "reconnecting": {
      color = "bg-warn";
      const s = c.retryAt ? Math.max(0, Math.ceil((c.retryAt - now) / 1000)) : 0;
      label = s > 0 ? `Reconnecting in ${s}s` : "Reconnecting…";
      break;
    }
    case "unauthorized":
      color = "bg-danger";
      label = "Token rejected";
      break;
    case "offline": {
      color = "bg-danger";
      const s = c.retryAt ? Math.max(0, Math.ceil((c.retryAt - now) / 1000)) : 0;
      label = s > 0 ? `Offline · retry in ${s}s` : "Offline";
      break;
    }
  }
  const clickable = c.status === "reconnecting" || c.status === "offline";
  return (
    <button
      onClick={clickable ? reconnectNow : undefined}
      aria-label={`Connection: ${label}`}
      data-testid="connection"
      title={c.error ? `${label} — ${c.error}${clickable ? " (click to retry now)" : ""}` : label}
      className={cx(
        "flex items-center gap-2 rounded-lg text-xs text-muted",
        compact ? "p-1.5" : "w-full px-2 py-1.5",
        clickable ? "cursor-pointer hover:bg-surface-2 hover:text-fg" : "cursor-default",
      )}
    >
      <span className="relative flex size-2">
        {c.status === "live" && <span className={cx("absolute inline-flex size-full animate-ping rounded-full opacity-40 [animation-duration:2.4s]", color)} />}
        <span className={cx("relative inline-flex size-2 rounded-full", color)} />
      </span>
      {!compact && <span className="truncate">{label}</span>}
    </button>
  );
}
