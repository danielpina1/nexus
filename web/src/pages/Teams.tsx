import { useMemo } from "react";
import { Link } from "react-router";
import { AlertTriangle, Bot, GitBranch, Plus, TriangleAlert, Users } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Card } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { buttonClass } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { TeamIcon } from "../components/team/TeamIcon";
import { StateIcon } from "../components/StateBadge";
import { agentName, useAgentMap } from "../components/agentMeta";
import { memberSlots, orchestratorLabel, strategyLabel, useAllTeamRuns, useTeamRunsSeed, useTeams } from "../lib/teams";
import { costMinor, cx, formatMoney, isActive, taskStateLabel, taskStateTone, timeAgo, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { prefetchPath } from "../app/registry";
import type { Team } from "../gen/nexus/v1/team_pb";
import type { Task } from "../gen/nexus/v1/task_pb";

const toneText = { neutral: "text-muted", accent: "text-accent", ok: "text-ok", warn: "text-warn", danger: "text-danger", info: "text-info" } as const;

function RunLine({ run, now }: { run: Task; now: number }) {
  const tone = taskStateTone(run.state);
  const cost = Math.max(costMinor(run.treeCost), costMinor(run.cost));
  return (
    <Link
      to={`/teams/runs/${run.id}`}
      onMouseEnter={() => prefetchPath(`/teams/runs/${run.id}`)}
      className="relative z-10 flex min-h-9 items-center gap-2 rounded-lg px-2 text-[12.5px] hover:bg-surface-2 pointer-coarse:min-h-11"
    >
      <span className={cx("shrink-0", toneText[tone])} title={taskStateLabel[run.state]}>
        <StateIcon state={run.state} className="size-3.5" />
        <span className="sr-only">{taskStateLabel[run.state]}</span>
      </span>
      <span className="min-w-0 flex-1 truncate text-fg-2">{run.title || run.prompt}</span>
      <span className="shrink-0 text-muted tabular">{formatMoney(cost, run.treeCost?.amount?.currency || run.cost?.amount?.currency)}</span>
      <span className="hidden w-16 shrink-0 text-right text-muted sm:block">{timeAgo(tsMs(run.createdAt), now)}</span>
    </Link>
  );
}

function TeamCard({ team, runs, now }: { team: Team; runs: Task[]; now: number }) {
  const agents = useAgentMap();
  const live = runs.filter((r) => isActive(r.state)).length;
  const orch = orchestratorLabel(team);
  return (
    <Card className="group relative flex h-full flex-col p-4 transition-colors duration-150 focus-within:border-border-strong hover:border-border-strong">
      <div className="flex items-start gap-3">
        <TeamIcon id={team.id} icon={team.icon} size="lg" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <h2 className="min-w-0 truncate text-[15px] font-semibold text-fg">
              <Link
                to={`/teams/${team.id}`}
                onMouseEnter={() => prefetchPath(`/teams/${team.id}`)}
                className="after:absolute after:inset-0 after:rounded-xl after:content-[''] focus-visible:outline-none"
                data-testid="team-card-link"
              >
                {team.name || team.id}
              </Link>
            </h2>
            {!team.enabled && <Badge>disabled</Badge>}
            {team.errors.length > 0 && (
              <Badge tone="danger" title={team.errors.join("\n")}>
                <AlertTriangle className="size-3" aria-hidden />
                {team.errors.length} {team.errors.length === 1 ? "error" : "errors"}
              </Badge>
            )}
            {team.warnings.length > 0 && (
              <Badge tone="warn" title={team.warnings.join("\n")}>
                <TriangleAlert className="size-3" aria-hidden />
                {team.warnings.length} {team.warnings.length === 1 ? "warning" : "warnings"}
              </Badge>
            )}
          </div>
          <div className="truncate font-mono text-xs text-muted">teams/{team.id}.md</div>
        </div>
        {live > 0 && (
          <Badge tone="info" dot pulse>
            {live} live
          </Badge>
        )}
      </div>

      <p className="mt-3 line-clamp-2 text-[13px] text-fg-2">{team.description}</p>

      {team.errors.length > 0 && (
        <p className="mt-2 flex items-start gap-1.5 text-[12.5px] text-danger">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span className="line-clamp-2">{team.errors[0]}</span>
        </p>
      )}

      <dl className="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[12.5px]">
        <dt className="text-muted">Strategy</dt>
        <dd>
          <Badge tone="accent">{strategyLabel[team.strategy]}</Badge>
        </dd>
        <dt className="text-muted">Orchestrator</dt>
        <dd className="flex min-w-0 items-center gap-1 text-fg">
          <Bot className="size-3.5 shrink-0 text-muted" aria-hidden />
          <span className="truncate">{team.orchestratorAgentId ? agentName(agents, team.orchestratorAgentId) : orch}</span>
        </dd>
        <dt className="text-muted">Members</dt>
        <dd className="flex min-w-0 flex-wrap gap-1">
          {team.members.map((m) => (
            <Badge key={m.role || m.agentId} title={`${m.role} · ${agentName(agents, m.agentId)}${m.when ? ` — ${m.when}` : ""}`}>
              {m.role || m.agentId}
              {m.count > 1 && <span className="text-muted">×{m.count}</span>}
            </Badge>
          ))}
          {team.members.length === 0 && <span className="text-muted">—</span>}
        </dd>
      </dl>

      <div className="mt-3 flex-1 border-t border-border pt-2">
        <div className="mb-1 flex items-center gap-2 px-2 text-[11px] font-semibold tracking-wide text-muted uppercase">
          <span className="flex-1">Recent runs</span>
          <span className="flex items-center gap-1 normal-case">
            <Users className="size-3" aria-hidden />
            {team.members.length} roles · {memberSlots(team)} slots
          </span>
        </div>
        {runs.length === 0 ? (
          <div className="px-2 py-1.5 text-[12.5px] text-muted">No runs yet.</div>
        ) : (
          runs.slice(0, 3).map((r) => <RunLine key={r.id} run={r} now={now} />)
        )}
      </div>
    </Card>
  );
}

export default function Teams() {
  const { data, isLoading, isError, error, refetch } = useTeams();
  const ids = useMemo(() => (data ?? []).map((t) => t.id), [data]);
  useTeamRunsSeed(ids, 5);
  const runs = useAllTeamRuns();
  const now = useNow(30_000);
  const live = useMemo(() => [...runs.values()].flat().filter((r) => isActive(r.state)).length, [runs]);

  return (
    <div>
      <PageHeader
        title="Teams"
        subtitle={
          <span>
            Swarms of agents under an orchestrator, working on one task tree. Teams are notes in <span className="font-mono">teams/*.md</span>.
            {live > 0 && (
              <span className="ml-1 inline-flex items-center gap-1 text-info">
                <GitBranch className="size-3" aria-hidden /> {live} running now
              </span>
            )}
          </span>
        }
        actions={
          <Link to="/teams/new" className={buttonClass("primary")}>
            <Plus className="size-3.5" aria-hidden />
            New team
          </Link>
        }
      />
      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-72" />
          ))}
        </div>
      ) : isError ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : !data?.length ? (
        <EmptyState
          icon={<Users />}
          title="No teams yet"
          body="A team is an orchestrator plus member agents with shared limits. Create one, or ask the Architect to design it."
          action={
            <div className="flex flex-wrap justify-center gap-2">
              <Link to="/teams/new" className={buttonClass("primary")}>
                <Plus className="size-3.5" aria-hidden />
                New team
              </Link>
              <Link to="/architect?kind=team" className={buttonClass()}>
                Design with the Architect
              </Link>
            </div>
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2" data-testid="team-list">
          {data.map((t) => (
            <TeamCard key={t.id} team={t} runs={runs.get(t.id) ?? []} now={now} />
          ))}
        </div>
      )}
    </div>
  );
}
