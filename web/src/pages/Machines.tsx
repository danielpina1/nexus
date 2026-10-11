import { Cpu } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { NodeCard } from "../components/NodeCard";
import { useNodes } from "../lib/queries";
import { useEvents } from "../lib/store";

export default function Machines() {
  const { data, isLoading, isError, error, refetch } = useNodes();
  const status = useEvents((s) => s.nodeStatus);
  const online = (data ?? []).filter((n) => (status[n.id] ?? n.status)?.online).length;
  return (
    <div>
      <PageHeader icon={<Cpu />} title="Machines" subtitle={data ? `${online} of ${data.length} nodes online · telemetry via the Hub` : "Nodes paired with your Hub"} />
      {isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
        </div>
      ) : isError ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : !data?.length ? (
        <EmptyState icon={<Cpu />} title="No machines yet" body="Install nexus-node on a machine and pair it with the Hub." />
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data.map((n) => (
            <NodeCard key={n.id} node={n} status={status[n.id]} />
          ))}
        </div>
      )}
    </div>
  );
}
