import { useMemo } from "react";
import { Link } from "react-router";
import { AlertTriangle, Bot, Plus } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Card } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { AgentIcon } from "../components/AgentIcon";
import { useAgents } from "../lib/queries";
import { useAllTasks } from "../lib/selectors";
import { costMinor, formatMoney, isActive, tierLabel, tsMs } from "../lib/format";

export default function Agents() {
  const { data, isLoading, isError, error, refetch } = useAgents();
  const tasks = useAllTasks();
  const stats = useMemo(() => {
    const since = Date.now() - 7 * 86_400_000;
    const m = new Map<string, { runs: number; cost: number; active: number }>();
    for (const t of tasks) {
      const s = m.get(t.agentId) ?? { runs: 0, cost: 0, active: 0 };
      if (tsMs(t.createdAt) >= since) {
        s.runs++;
        s.cost += costMinor(t.cost);
      }
      if (isActive(t.state)) s.active++;
      m.set(t.agentId, s);
    }
    return m;
  }, [tasks]);

  return (
    <div>
      <PageHeader
        icon={<Bot />}
        title="Agents"
        subtitle="Agents are Markdown notes in your vault (agents/*.md). Edit them here or in any editor."
        actions={
          <Link to="/agents/new">
            <Button variant="primary" icon={<Plus className="size-3.5" />}>
              New agent
            </Button>
          </Link>
        }
      />
      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-48" />
          ))}
        </div>
      ) : isError ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : !data?.length ? (
        <EmptyState icon={<Bot />} title="No agents yet" body="Create one, or add a note under agents/ in the vault." />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {data.map((a) => {
            const s = stats.get(a.id);
            return (
              <Link key={a.id} to={`/agents/${a.id}`}>
                <Card className="flex h-full flex-col p-4 transition-colors hover:border-border-strong">
                  <div className="flex items-start gap-3">
                    <AgentIcon agentId={a.id} icon={a.icon} size="lg" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate text-[15px] font-semibold text-fg">{a.name}</span>
                        {!a.enabled && <Badge>disabled</Badge>}
                        {a.errors.length > 0 && (
                          <Badge tone="danger">
                            <AlertTriangle className="size-3" />
                            {a.errors.length}
                          </Badge>
                        )}
                      </div>
                      <div className="font-mono text-xs text-muted">{a.id}</div>
                    </div>
                    {(s?.active ?? 0) > 0 && (
                      <Badge tone="info" dot pulse>
                        {s?.active} live
                      </Badge>
                    )}
                  </div>
                  <p className="mt-3 line-clamp-3 flex-1 text-[13px] text-fg-2">{a.description}</p>
                  <div className="mt-3 flex flex-wrap gap-1">
                    <Badge tone="accent">{a.runtime || "—"}</Badge>
                    {a.model && <Badge>{a.model}</Badge>}
                    <Badge>{tierLabel[a.tier]}</Badge>
                    {a.nodeTags.map((t) => (
                      <Badge key={t}>#{t}</Badge>
                    ))}
                  </div>
                  <div className="mt-3 flex items-center gap-3 border-t border-border pt-3 text-xs text-muted tabular">
                    <span>{s?.runs ?? 0} runs / 7d</span>
                    <span>{formatMoney(s?.cost ?? 0)}</span>
                    <span>{a.tools.length} tools</span>
                    <span className="flex-1" />
                    <span>v{a.version}</span>
                  </div>
                </Card>
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}
