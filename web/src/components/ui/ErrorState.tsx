import { CloudOff } from "lucide-react";
import { errorMessage } from "../../lib/toast";
import { Button } from "./Button";
import { EmptyState } from "./EmptyState";

export function ErrorState({ error, onRetry, compact }: { error: unknown; onRetry?: () => void; compact?: boolean }) {
  return (
    <EmptyState
      compact={compact}
      icon={<CloudOff />}
      title="Couldn't reach the Hub"
      body={<span className="font-mono text-xs break-all">{errorMessage(error)}</span>}
      action={onRetry && <Button onClick={onRetry}>Retry</Button>}
    />
  );
}
