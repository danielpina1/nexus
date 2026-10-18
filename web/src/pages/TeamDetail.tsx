import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { AlertTriangle, ArrowLeft, Bot, Gauge, History, Play, Sparkles, TriangleAlert, Users } from "lucide-react";
import { splitFrontmatter } from "../lib/yaml";
import { humanSeconds, strategyHint, strategyLabel, useRunTeam, useRunsOf, useTeam, useTeamRunsSeed } from "../lib/teams";
import { costMinor, formatDuration, formatMoney, isActive, timeAgo, tierLabel, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { prefetchPath } from "../app/registry";
import { Card, CardHeader } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { Textarea } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { Skeleton, SkeletonRows } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { Markdown } from "../components/Markdown";
import { AgentIcon } from "../components/AgentIcon";
import { StateBadge } from "../components/StateBadge";
import { VirtualList } from "../components/VirtualList";
import { TeamIcon } from "../components/team/TeamIcon";
import { TEAM_TEMPLATE, TeamEditor } from "../components/team/TeamEditor";
import { agentName, useAgentMap } from "../components/agentMeta";
import type { Team } from "../gen/nexus/v1/team_pb";
import type { Task } from "../gen/nexus/v1/task_pb";

type Tab = "overview" | "note" | "edit";

function RunRow({ run, now }: { run: Task; now: number }) {
  const active = isActive(run.state);
  const dur = (active ? now : tsMs(run.updatedAt)) - tsMs(run.createdAt);
  const cost = Math.max(costMinor(run.treeCost), costMinor(run.cost));
  return (
    <Link
      to={`/teams/runs/${run.id}`}
      onMouseEnter={() => prefetchPath(`/teams/runs/${run.id}`)}
      onFocus={() => prefetchPath(`/teams/runs/${run.id}`)}
      className="flex min-h-[56px] items-center gap-3 px-4 py-2 transition-colors duration-150 hover:bg-surface-2"
    >
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] font-medium text-fg">{run.title || run.prompt}</div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-xs text-muted tabular">
          <span>{timeAgo(tsMs(run.createdAt), now)}</span>
          <span aria-hidden>·</span>
          <span>{formatDuration(dur)}</span>
          {run.phase && (
            <>
              <span aria-hidden>·</span>
              <span>{run.phase.replace(/_/g, " ")}</span>
            </>
          )}
        </div>
      </div>
      <span className="hidden text-xs text-fg-2 tabular sm:block">{formatMoney(cost, run.treeCost?.amount?.currency)}</span>
      <StateBadge state={run.state} />
    </Link>
  );
}

function RunBox({ team }: { team: Team }) {
  const [prompt, setPrompt] = useState("");
  const run = useRunTeam();
  const navigate = useNavigate();
  const blocked = team.errors.length > 0 ? "This team has errors; fix the note first." : !team.enabled ? "This team is disabled." : "";
  const submit = () => {
    const p = prompt.trim();
    if (!p || blocked) return;
    run.mutate(
      { teamId: team.id, prompt: p },
      {
        onSuccess: (r) => {
          setPrompt("");
          if (r.task) navigate(`/teams/runs/${r.task.id}`);
        },
      },
    );
  };
  return (
    <Card className="p-4">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label htmlFor="team-prompt" className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-fg">
          <Play className="size-3.5 text-accent" aria-hidden /> Run this team
        </label>
        <Textarea
          id="team-prompt"
          data-testid="team-run-prompt"
          rows={3}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
          placeholder={team.triggers.length ? `e.g. "${team.triggers[0]}: …"` : "What should the team do?"}
          className="resize-y"
          disabled={!!blocked}
          aria-describedby="team-prompt-hint"
        />
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <p id="team-prompt-hint" className={blocked ? "min-w-0 flex-1 text-xs text-danger" : "min-w-0 flex-1 text-xs text-muted"}>
            {blocked || `Creates a task for the team; ${team.orchestratorAgentId || "the orchestrator"} plans and delegates within the limits. ⌘↵ to run.`}
          </p>
          <Button type="submit" variant="primary" icon={<Play className="size-3.5" />} loading={run.isPending} disabled={!prompt.trim() || !!blocked} data-testid="team-run-submit">
            Run team
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Overview({ team, markdown }: { team: Team; markdown: string }) {
  const agents = useAgentMap();
  const runs = useRunsOf(team.id);
  const seed = useTeamRunsSeed([team.id], 50);
  const now = useNow(15_000);
  const body = useMemo(() => splitFrontmatter(markdown).body, [markdown]);
  const l = team.limits;
  const io = team.inlineOrchestrator;
  const totalCost = runs.reduce((s, r) => s + Math.max(costMinor(r.treeCost), costMinor(r.cost)), 0);

  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
      <div className="min-w-0 space-y-5">
        <RunBox team={team} />
        <Card className="overflow-hidden">
          <CardHeader
            icon={<History className="size-4" />}
            title="Runs"
            subtitle={runs.length ? `${runs.length} ${runs.length === 1 ? "run" : "runs"} · ${formatMoney(totalCost)} in total` : "No runs yet"}
          />
          {seed.isLoading && runs.length === 0 ? (
            <SkeletonRows rows={3} />
          ) : seed.isError && runs.length === 0 ? (
            <ErrorState compact error={seed.error} />
          ) : runs.length === 0 ? (
            <EmptyState compact icon={<History />} title="No runs yet" body="Run the team above; its runs appear here live." />
          ) : (
            <VirtualList className="divide-y divide-border" items={runs} getKey={(r) => r.id} estimate={57} render={(r) => <RunRow run={r} now={now} />} />
          )}
        </Card>
        <Card className="p-5 sm:p-6">
          <div className="mb-3 text-[11px] font-semibold tracking-wide text-muted uppercase">Mission & instructions</div>
          {body.trim() ? <Markdown>{body}</Markdown> : <p className="text-[13px] text-muted">The note has no body.</p>}
        </Card>
      </div>

      <div className="space-y-5">
        <Card>
          <CardHeader icon={<Bot className="size-4" />} title="Orchestrator" subtitle={`${strategyLabel[team.strategy]} — ${strategyHint[team.strategy]}`} className="[&_.truncate]:whitespace-normal" />
          <div className="p-3">
            {team.orchestratorAgentId ? (
              <Link to={`/agents/${team.orchestratorAgentId}`} className="flex min-h-11 items-center gap-2.5 rounded-lg px-1.5 hover:bg-surface-2">
                <AgentIcon agentId={team.orchestratorAgentId} icon={agents[team.orchestratorAgentId]?.icon} size="sm" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-medium text-fg">{agentName(agents, team.orchestratorAgentId)}</span>
                  <span className="block truncate text-xs text-muted">{agents[team.orchestratorAgentId]?.runtime || "agent"} · orchestrator</span>
                </span>
              </Link>
            ) : io?.instructions ? (
              <div className="space-y-2 px-1.5 py-1 text-[13px]">
                <div className="flex flex-wrap gap-1">
                  <Badge tone="accent">inline</Badge>
                  {io.runtime && <Badge>{io.runtime}</Badge>}
                  {io.model && <Badge>{io.model}</Badge>}
                  <Badge>{tierLabel[io.tier]}</Badge>
                </div>
                <details className="text-fg-2">
                  <summary className="cursor-pointer text-xs text-muted hover:text-fg">Instructions</summary>
                  <p className="mt-1.5 text-[12.5px] whitespace-pre-wrap">{io.instructions}</p>
                </details>
              </div>
            ) : (
              <p className="px-1.5 py-1 text-[13px] text-muted">No orchestrator — the hub drives the {strategyLabel[team.strategy]} strategy.</p>
            )}
          </div>
        </Card>

        <Card>
          <CardHeader icon={<Users className="size-4" />} title="Members" subtitle={`${team.members.length} roles`} />
          <ul className="divide-y divide-border">
            {team.members.map((m) => (
              <li key={m.role || m.agentId} className="flex gap-2.5 px-4 py-2.5">
                <AgentIcon agentId={m.agentId} icon={agents[m.agentId]?.icon} size="sm" className="mt-0.5" />
                <div className="min-w-0 flex-1 text-[13px]">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="font-semibold text-fg">{m.role || m.agentId}</span>
                    {m.count > 1 && <Badge title="Worker pool: at most this many active at once">×{m.count}</Badge>}
                    <Link to={`/agents/${m.agentId}`} className="truncate text-xs text-muted hover:text-fg hover:underline">
                      {agentName(agents, m.agentId)}
                    </Link>
                  </div>
                  {m.when && <p className="mt-0.5 text-xs text-fg-2">{m.when}</p>}
                </div>
              </li>
            ))}
            {team.members.length === 0 && <li className="px-4 py-3 text-[13px] text-muted">No members.</li>}
          </ul>
        </Card>

        <Card>
          <CardHeader icon={<Gauge className="size-4" />} title="Limits" subtitle="Enforced by the hub for the whole run" />
          <dl className="grid grid-cols-2 gap-px bg-border text-[13px]">
            {[
              ["Parallel", l?.maxParallel || "default"],
              ["Tasks", l?.maxTasks || "default"],
              ["Depth", l?.maxDepth || "default"],
              ["Runs", l?.maxRuns || "default"],
              ["Budget", l?.maxCost ? formatMoney(Number(l.maxCost.minorUnits), l.maxCost.currency) : "default"],
              ["Timeout", l?.timeoutSeconds ? humanSeconds(Number(l.timeoutSeconds)) : "default"],
            ].map(([k, v]) => (
              <div key={String(k)} className="bg-surface px-4 py-2">
                <dt className="text-xs text-muted">{k}</dt>
                <dd className="font-heading text-[15px] font-semibold text-fg tabular">{v}</dd>
              </div>
            ))}
          </dl>
          <div className="flex flex-wrap gap-1 px-4 py-3">
            <Badge tone={team.blackboard ? "accent" : "neutral"}>blackboard {team.blackboard ? "on" : "off"}</Badge>
            {team.bundleApprovals && <Badge>bundled approvals</Badge>}
            {team.debateRounds > 0 && <Badge>{team.debateRounds} debate rounds</Badge>}
            {team.outputSchema && <Badge>structured output</Badge>}
          </div>
        </Card>

        {team.triggers.length > 0 && (
          <Card className="p-4">
            <div className="mb-2 text-[11px] font-semibold tracking-wide text-muted uppercase">Router phrases</div>
            <div className="flex flex-wrap gap-1">
              {team.triggers.map((t) => (
                <Badge key={t}>“{t}”</Badge>
              ))}
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}

export default function TeamDetail() {
  const { id = "" } = useParams();
  const isNew = id === "new";
  const q = useTeam(isNew ? undefined : id);
  const [params, setParams] = useSearchParams();
  const tab: Tab = isNew ? "edit" : ((params.get("tab") as Tab) || "overview");
  const setTab = (t: Tab) => setParams(t === "overview" ? {} : { tab: t }, { replace: true });

  if (!isNew && q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  if (!isNew && !q.data) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-72" />
        <Skeleton className="h-40" />
        <Skeleton className="h-[360px]" />
      </div>
    );
  }
  const team = q.data?.team;
  const markdown = q.data?.markdown ?? "";

  return (
    <div>
      <Link to="/teams" className="mb-3 inline-flex min-h-8 items-center gap-1 text-[13px] text-muted hover:text-fg">
        <ArrowLeft className="size-3.5" aria-hidden /> Teams
      </Link>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <TeamIcon id={team?.id || "new"} icon={team?.icon} size="lg" />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[22px] leading-tight font-semibold tracking-[-0.01em] text-fg">{isNew ? "New team" : team?.name || id}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted">
            <span className="font-mono">{team?.note?.path || `teams/${isNew ? "…" : id}.md`}</span>
            {team && (
              <>
                <Badge tone="accent">{strategyLabel[team.strategy]}</Badge>
                {team.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>disabled</Badge>}
                <span>v{team.version}</span>
              </>
            )}
          </div>
        </div>
        {isNew && (
          <Link to="/architect?kind=team" className="inline-flex min-h-9 items-center gap-1.5 rounded-lg px-2 text-[13px] text-accent hover:underline">
            <Sparkles className="size-3.5" aria-hidden /> Let the Architect design it
          </Link>
        )}
      </div>

      {team && team.errors.length > 0 && (
        <Card className="mb-5 border-danger/40 p-4" role="alert">
          <div className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-danger">
            <AlertTriangle className="size-4" aria-hidden /> This team is disabled until these are fixed
          </div>
          <ul className="list-disc space-y-0.5 pl-5 text-[13px] text-fg-2">
            {team.errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </Card>
      )}
      {team && team.warnings.length > 0 && (
        <Card className="mb-5 border-warn/40 p-4">
          <div className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-warn">
            <TriangleAlert className="size-4" aria-hidden /> Warnings
          </div>
          <ul className="list-disc space-y-0.5 pl-5 text-[13px] text-fg-2">
            {team.warnings.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </Card>
      )}

      {!isNew && (
        <Tabs<Tab>
          className="mb-4"
          label="Team views"
          value={tab}
          onChange={setTab}
          items={[
            { id: "overview", label: "Overview" },
            { id: "note", label: "Note" },
            { id: "edit", label: "Edit" },
          ]}
        />
      )}

      {tab === "overview" && team && <Overview team={team} markdown={markdown} />}
      {tab === "note" && (
        <Card className="p-5 sm:p-6">
          <Markdown>{splitFrontmatter(markdown).body || "_(empty)_"}</Markdown>
          <details className="mt-5 border-t border-border pt-3">
            <summary className="cursor-pointer text-[13px] text-muted hover:text-fg">Frontmatter (raw)</summary>
            <pre className="mt-2 overflow-x-auto rounded-lg bg-surface-2 p-3 font-mono text-[12px] text-fg-2 scrollbar-thin">{markdown.split(/\n---/)[0].replace(/^---\n?/, "")}</pre>
          </details>
        </Card>
      )}
      {tab === "edit" && <TeamEditor key={isNew ? "new" : `${id}:${team?.version}`} initial={isNew ? TEAM_TEMPLATE : markdown} isNew={isNew} teamId={isNew ? undefined : id} />}
    </div>
  );
}
