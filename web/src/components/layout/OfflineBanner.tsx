import { Link } from "react-router";
import { CloudOff } from "lucide-react";
import { useEvents } from "../../lib/store";
import { reconnectNow } from "../../lib/events";
import { hubBaseUrl, isMock } from "../../lib/api";

/** Shown when the event stream can't reach the Hub (never in mock mode). */
export function OfflineBanner() {
  const c = useEvents((s) => s.connection);
  if (isMock() || (c.status !== "offline" && !(c.status === "reconnecting" && c.attempt >= 2))) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-warn/30 bg-warn-soft px-4 py-2 text-[13px] text-warn">
      <CloudOff className="size-4 shrink-0" />
      <span className="min-w-0 flex-1">
        Can't reach the Hub at <span className="font-mono">{hubBaseUrl()}</span>
        {c.error ? <span className="opacity-80"> — {c.error}</span> : null}
      </span>
      <button onClick={reconnectNow} className="font-medium underline-offset-2 hover:underline">
        Retry now
      </button>
      <Link to="/settings" className="font-medium underline-offset-2 hover:underline">
        Connection settings
      </Link>
    </div>
  );
}
