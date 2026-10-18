import { useEffect, useId, useState } from "react";
import { useNavigate } from "react-router";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";
import { AlertTriangle, CheckCircle2, Loader2, RotateCcw, Save, TriangleAlert } from "lucide-react";
import { api } from "../../lib/api";
import { toast } from "../../lib/toast";
import { limitsSummary, orchestratorLabel, strategyLabel, teamKeys } from "../../lib/teams";
import { Card, CardHeader } from "../ui/Card";
import { Badge } from "../ui/Badge";
import { Button } from "../ui/Button";
import { Input, Textarea } from "../ui/Input";
import type { Team } from "../../gen/nexus/v1/team_pb";

export const TEAM_TEMPLATE = `---
type: team
id: my-team
name: My team
description: What this team does and when the router should pick it (the router reads this).
icon: person.3
orchestrator: research-lead
strategy: hierarchical
members:
  - { agent: researcher, role: researcher, count: 2, when: "one sub-question each" }
  - { agent: fact-checker, role: fact-checker, when: "once a draft exists" }
shared: { blackboard: true }
limits: { max_parallel: 2, max_tasks: 10, max_depth: 2, max_cost_eur: 2, timeout: 30m, max_runs: 20 }
approvals: { bundle: true }
triggers: ["my team"]
version: 1
---

# My team

## Mission
What the team delivers, and how good is good enough.

## Operating instructions (for the orchestrator)
1. Split the work and delegate it with \`team.delegate_many\`.
2. Write the plan to the blackboard (\`plan\`).
3. \`team.await\` the results and synthesise one answer.
`;

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

function errLines(err: unknown): string[] {
  if (err instanceof ConnectError) return (err.rawMessage || err.message).split("\n").filter(Boolean);
  return [err instanceof Error ? err.message : String(err)];
}

function Parsed({ team }: { team: Team }) {
  const rows: Array<[string, React.ReactNode]> = [
    ["Name", team.name || "—"],
    ["ID", <span className="font-mono">{team.id || "—"}</span>],
    ["Strategy", <Badge tone="accent">{strategyLabel[team.strategy]}</Badge>],
    ["Orchestrator", orchestratorLabel(team)],
    [
      "Members",
      team.members.length ? (
        <div className="flex flex-wrap gap-1">
          {team.members.map((m) => (
            <Badge key={m.role || m.agentId}>
              {m.role || m.agentId} · {m.agentId}
              {m.count > 1 ? ` ×${m.count}` : ""}
            </Badge>
          ))}
        </div>
      ) : (
        "—"
      ),
    ],
    [
      "Limits",
      <div className="flex flex-wrap gap-1">
        {limitsSummary(team.limits).map((l) => (
          <Badge key={l.label}>
            {l.label} {l.value}
          </Badge>
        ))}
      </div>,
    ],
    ["Blackboard", team.blackboard ? "shared" : "off"],
  ];
  return (
    <dl className="divide-y divide-border/60">
      {rows.map(([k, v]) => (
        <div key={k} className="grid grid-cols-[100px_minmax(0,1fr)] gap-3 py-1.5 text-[13px]">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0 break-words text-fg">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * Markdown editor for a team note with live server validation (UpsertTeam{validate_only}) while you
 * type; saving is blocked while the Hub reports errors.
 */
export function TeamEditor({ initial, isNew, teamId }: { initial: string; isNew: boolean; teamId?: string }) {
  const [text, setText] = useState(initial);
  const [commitMsg, setCommitMsg] = useState("");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const errorsId = useId();
  const dirty = isNew || text !== initial;
  const debounced = useDebounced(text, 450);

  const check = useQuery({
    queryKey: ["team-validate", debounced],
    queryFn: async () => (await api().teams.upsertTeam({ markdown: debounced, validateOnly: true })).team,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: Infinity,
  });
  const checking = check.isFetching || debounced !== text;
  const parsed = check.data;
  const errors = check.isError ? errLines(check.error) : (parsed?.errors ?? []);
  const warnings = check.isError ? [] : (parsed?.warnings ?? []);
  const valid = !check.isError && !!parsed && errors.length === 0;

  const save = useMutation({
    mutationFn: async () =>
      api().teams.upsertTeam({ markdown: text, commitMessage: commitMsg || `${isNew ? "Create" : "Update"} team ${parsed?.id ?? teamId ?? ""}`.trim() }),
    onSuccess: async (res) => {
      const t = res.team;
      toast.success(isNew ? "Team created" : "Team saved", t ? `${t.name} v${t.version}` : undefined);
      setCommitMsg("");
      await qc.invalidateQueries({ queryKey: teamKeys.teams });
      if (t) {
        await qc.invalidateQueries({ queryKey: teamKeys.team(t.id) });
        navigate(`/teams/${t.id}`, { replace: isNew || t.id !== teamId });
      }
    },
    onError: (err) => toast.error("The Hub rejected this team", err),
  });
  const canSave = dirty && valid && !checking && !save.isPending;

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1 text-[13px] text-muted">
          {checking ? (
            <span className="inline-flex items-center gap-1.5">
              <Loader2 className="size-3.5 animate-spin" aria-hidden /> Validating with the Hub…
            </span>
          ) : valid ? (
            <span className="inline-flex items-center gap-1.5 text-ok">
              <CheckCircle2 className="size-3.5" aria-hidden /> Valid{warnings.length ? ` · ${warnings.length} warning${warnings.length === 1 ? "" : "s"}` : ""}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1.5 text-danger">
              <AlertTriangle className="size-3.5" aria-hidden /> {errors.length} {errors.length === 1 ? "problem" : "problems"} — fix before saving
            </span>
          )}
        </div>
        {dirty && !isNew && (
          <Button variant="ghost" icon={<RotateCcw className="size-3.5" />} onClick={() => setText(initial)}>
            Revert
          </Button>
        )}
        <Input value={commitMsg} onChange={(e) => setCommitMsg(e.target.value)} placeholder="Commit message (optional)" aria-label="Commit message" className="w-full sm:w-56" />
        <Button variant="primary" icon={<Save className="size-3.5" />} loading={save.isPending} disabled={!canSave} onClick={() => save.mutate()} data-testid="team-save">
          {isNew ? "Create team" : "Save"}
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1fr)_380px]">
        <Card className="overflow-hidden">
          <CardHeader
            title="Team note"
            subtitle={`teams/${parsed?.id || teamId || "…"}.md · frontmatter + mission. Saved as a git commit.`}
            actions={dirty ? <Badge tone="warn">unsaved</Badge> : <Badge tone="ok">saved</Badge>}
          />
          <Textarea
            value={text}
            aria-label="Team note markdown"
            aria-invalid={errors.length > 0 && !checking}
            aria-describedby={errors.length ? errorsId : undefined}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === "s") {
                e.preventDefault();
                if (canSave) save.mutate();
              }
            }}
            spellCheck={false}
            data-testid="team-markdown"
            className="min-h-[460px] resize-y rounded-none border-0 bg-surface font-mono text-[12.5px] leading-relaxed sm:min-h-[560px]"
          />
        </Card>
        <div className="space-y-5">
          {errors.length > 0 && (
            <Card className="border-danger/40 p-4" role="alert" id={errorsId}>
              <div className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-danger">
                <AlertTriangle className="size-4" aria-hidden /> {errors.length === 1 ? "1 problem" : `${errors.length} problems`}
              </div>
              <ul className="list-disc space-y-1 pl-5 text-[13px] text-fg-2">
                {errors.map((e) => (
                  <li key={e}>{e}</li>
                ))}
              </ul>
            </Card>
          )}
          {warnings.length > 0 && (
            <Card className="border-warn/40 p-4">
              <div className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-warn">
                <TriangleAlert className="size-4" aria-hidden /> Warnings
              </div>
              <ul className="list-disc space-y-1 pl-5 text-[13px] text-fg-2">
                {warnings.map((e) => (
                  <li key={e}>{e}</li>
                ))}
              </ul>
            </Card>
          )}
          <Card>
            <CardHeader title="Parsed by the Hub" subtitle="Live: what the Hub will load from this note" />
            <div className="px-4 py-2">{parsed ? <Parsed team={parsed} /> : <div className="py-6 text-center text-[13px] text-muted">{checking ? "Validating…" : "Nothing parsed yet."}</div>}</div>
          </Card>
        </div>
      </div>
    </div>
  );
}
