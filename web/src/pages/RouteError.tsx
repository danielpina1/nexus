import { isRouteErrorResponse, Link, useRouteError } from "react-router";
import { TriangleAlert } from "lucide-react";
import { Button } from "../components/ui/Button";

export function RouteError() {
  const err = useRouteError();
  const msg = isRouteErrorResponse(err) ? `${err.status} ${err.statusText}` : err instanceof Error ? err.message : String(err);
  const chunk = /dynamically imported module|Importing a module script failed/i.test(msg);
  return (
    <div className="flex h-dvh flex-col items-center justify-center gap-3 bg-bg p-6 text-center">
      <TriangleAlert className="size-8 text-warn" />
      <div className="text-lg font-semibold text-fg">{chunk ? "A new version of Nexus is available" : "Something went wrong"}</div>
      <div className="max-w-md font-mono text-xs text-muted">{msg}</div>
      <div className="flex gap-2">
        <Button variant="primary" onClick={() => location.reload()}>
          Reload
        </Button>
        <Link to="/">
          <Button>Go to Overview</Button>
        </Link>
      </div>
    </div>
  );
}
