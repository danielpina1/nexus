import { Cpu } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { NodeCard } from "../components/NodeCard";
import { useNodes } from "../lib/queries";
import { useEvents } from "../lib/store";
import { useRunningTasks } from "../lib/selectors";
import { Card, CardHeader } from "../components/ui/Card";
import { TaskRow } from "../components/TaskRow";

export default function Machines() {
  const { data, isLoading, isError, error, refetch } = useNodes();
  const status = useEvents((s) => s.nodeStatus);
  const running = useRunningTasks().filter((t) => t.nodeId);
  const online = (data ?? []).filter((n) => (status[n.id] ?? n.status)?.online).length;
  return (
    <div>
      <PageHeader icon={<Cpu />} title="Machines" subtitle={data ? `${online} of ${data.length} nodes online · telemetry via the Hub` : "Nodes paired with your Hub"} />
      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
        </div>
      ) : isError ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : !data?.length ? (
        <EmptyState icon={<Cpu />} title="No machines yet" body="Install nexus-node on a machine and pair it with the Hub." />
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data.map((n) => (
            <NodeCard key={n.id} node={n} status={status[n.id]} />
          ))}
        </div>
      )}
      <Card className="mt-6 overflow-hidden">
        <CardHeader title="Running on your machines" subtitle="Live, from the Hub's event stream" />
        {running.length === 0 ? (
          <div className="p-6 text-center text-xs text-muted">All machines idle.</div>
        ) : (
          <div className="divide-y divide-border">
            {running.map((t) => (
              <TaskRow key={t.id} task={t} />
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}
