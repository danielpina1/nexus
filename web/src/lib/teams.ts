import { useEffect, useMemo } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { toJson } from "@bufbuild/protobuf";
import { ValueSchema, type Value } from "@bufbuild/protobuf/wkt";
import { api } from "./api";
import { useEvents } from "./store";
import { toast } from "./toast";
import { costMinor, isActive, tsMs } from "./format";
import { TeamEventKind, type Event, type TeamEvent } from "../gen/nexus/v1/event_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { TeamStrategy, type BlackboardEntry, type Team, type TeamLimits, type TeamMessage, type TeamRun } from "../gen/nexus/v1/team_pb";

export const teamKeys = {
  teams: ["teams"] as const,
  team: (id: string) => ["team", id] as const,
  runs: (teamId: string) => ["team-runs", teamId] as const,
  run: (rootId: string) => ["team-run", rootId] as const,
};

export const strategyLabel: Record<TeamStrategy, string> = {
  [TeamStrategy.UNSPECIFIED]: "hierarchical",
  [TeamStrategy.HIERARCHICAL]: "hierarchical",
  [TeamStrategy.PARALLEL]: "parallel",
  [TeamStrategy.PIPELINE]: "pipeline",
  [TeamStrategy.DEBATE]: "debate",
  [TeamStrategy.AUTO]: "auto",
};

export const strategyHint: Record<TeamStrategy, string> = {
  [TeamStrategy.UNSPECIFIED]: "The orchestrator plans, delegates, awaits and synthesises.",
  [TeamStrategy.HIERARCHICAL]: "The orchestrator plans, delegates, awaits and synthesises.",
  [TeamStrategy.PARALLEL]: "Every member gets the task at once; the orchestrator merges the results.",
  [TeamStrategy.PIPELINE]: "Members run in order, each consuming the previous output.",
  [TeamStrategy.DEBATE]: "Members argue over several rounds; a judge decides.",
  [TeamStrategy.AUTO]: "The orchestrator picks: delegate itself or let the hub run a fixed pattern.",
};

/** Who runs the team: an agent id, "inline" or nobody (hub-driven strategies). */
export function orchestratorLabel(t: Team): string {
  if (t.orchestratorAgentId) return t.orchestratorAgentId;
  if (t.inlineOrchestrator?.instructions) return "inline orchestrator";
  return "none (hub-driven)";
}

export function memberSlots(t: Team): number {
  return t.members.reduce((n, m) => n + Math.max(1, m.count || 1), 0);
}

// ---- queries --------------------------------------------------------------------------------------

export function useTeams() {
  return useQuery({
    queryKey: teamKeys.teams,
    queryFn: async () => (await api().teams.listTeams({ includeDisabled: true })).teams,
  });
}

export function useTeam(id: string | undefined) {
  return useQuery({
    queryKey: teamKeys.team(id ?? ""),
    enabled: !!id,
    queryFn: async () => api().teams.getTeam({ id: id! }),
  });
}

/** Seeds the event store with a team's runs (root tasks); the store keeps them live. */
export function useTeamRunsSeed(teamIds: string[], pageSize = 20) {
  const upsert = useEvents((s) => s.upsertTasks);
  const results = useQueries({
    queries: teamIds.map((id) => ({
      queryKey: [...teamKeys.runs(id), pageSize],
      queryFn: async () => (await api().tasks.listTasks({ teamId: id, rootsOnly: true, page: { pageSize } })).tasks,
    })),
  });
  const stamp = results.map((r) => r.dataUpdatedAt).join(",");
  useEffect(() => {
    for (const r of results) if (r.data) upsert(r.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-run only when a query got new data
  }, [upsert, stamp]);
  return { isLoading: results.some((r) => r.isLoading), isError: results.some((r) => r.isError), error: results.find((r) => r.error)?.error };
}

/** A team's runs (root tasks), newest first, live from the store. */
export function useRunsOf(teamId: string | undefined): Task[] {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(() => {
    if (!teamId) return [];
    return Object.values(tasks)
      .filter((t) => t.teamId === teamId && !t.parentId)
      .sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt));
  }, [tasks, teamId]);
}

export function useAllTeamRuns(): Map<string, Task[]> {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(() => {
    const m = new Map<string, Task[]>();
    for (const t of Object.values(tasks)) {
      if (!t.teamId || t.parentId) continue;
      const l = m.get(t.teamId);
      if (l) l.push(t);
      else m.set(t.teamId, [t]);
    }
    for (const l of m.values()) l.sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt));
    return m;
  }, [tasks]);
}

export function useTeamRunQuery(rootId: string | undefined) {
  const upsert = useEvents((s) => s.upsertTasks);
  const q = useQuery({
    queryKey: teamKeys.run(rootId ?? ""),
    enabled: !!rootId,
    queryFn: async () => (await api().teams.getTeamRun({ rootTaskId: rootId! })).run,
  });
  useEffect(() => {
    if (q.data) upsert([...(q.data.root ? [q.data.root] : []), ...q.data.tasks.flatMap((t) => (t.task ? [t.task] : []))]);
  }, [q.data, upsert]);
  return q;
}

// ---- live derivations ---------------------------------------------------------------------------

/** All tasks of a tree, live from the store (seeded by GetTeamRun + the tree subscription). */
export function useTreeTasks(rootId: string | undefined): Task[] {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(() => {
    if (!rootId) return [];
    return Object.values(tasks)
      .filter((t) => t.id === rootId || t.rootId === rootId)
      .sort((a, b) => tsMs(a.createdAt) - tsMs(b.createdAt) || (a.id < b.id ? -1 : 1));
  }, [tasks, rootId]);
}

export interface TeamEventItem {
  seq: bigint;
  at: number;
  ev: TeamEvent;
}

const EMPTY: Event[] = [];

/** TeamEvents of a run (published on the root task), oldest first. */
export function useTeamEvents(rootId: string | undefined): TeamEventItem[] {
  const tl = useEvents((s) => (rootId ? (s.timelines[rootId] ?? EMPTY) : EMPTY));
  return useMemo(() => {
    const out: TeamEventItem[] = [];
    for (const e of tl) if (e.body.case === "team") out.push({ seq: e.seq, at: tsMs(e.at), ev: e.body.value });
    return out;
  }, [tl]);
}

export function valueText(v: Value | undefined): string {
  if (!v) return "";
  const j = toJson(ValueSchema, v);
  return typeof j === "string" ? j : JSON.stringify(j, null, 2);
}

export interface BoardItem {
  key: string;
  text: string;
  role: string;
  taskId: string;
  version: number;
  at: number;
  /** Shown from the event before GetTeamRun caught up (the event text may be truncated). */
  partial?: boolean;
}

/** Blackboard: GetTeamRun's entries, overlaid with newer BLACKBOARD_UPDATED events (instant). */
export function mergeBoard(entries: BlackboardEntry[] | undefined, events: TeamEventItem[]): BoardItem[] {
  const m = new Map<string, BoardItem>();
  for (const e of entries ?? []) {
    m.set(e.key, { key: e.key, text: valueText(e.value), role: e.authorRole, taskId: e.authorTaskId, version: e.version, at: tsMs(e.updatedAt) });
  }
  for (const { ev, at } of events) {
    if (ev.kind !== TeamEventKind.BLACKBOARD_UPDATED || !ev.key) continue;
    const version = Number(ev.data?.version ?? 0);
    const cur = m.get(ev.key);
    if (cur && cur.version >= version && version > 0) continue;
    let text = ev.text;
    try {
      const j = JSON.parse(ev.text);
      text = typeof j === "string" ? j : JSON.stringify(j, null, 2);
    } catch {
      /* truncated JSON: show as is */
    }
    m.set(ev.key, { key: ev.key, text, role: ev.role, taskId: ev.taskId, version: version || (cur?.version ?? 0) + 1, at, partial: true });
  }
  return [...m.values()].sort((a, b) => b.at - a.at);
}

export interface MessageItem {
  id: string;
  from: string;
  to: string;
  text: string;
  at: number;
  delivered: number;
}

export function mergeMessages(list: TeamMessage[] | undefined, events: TeamEventItem[]): MessageItem[] {
  const m = new Map<string, MessageItem>();
  for (const x of list ?? []) m.set(x.id, { id: x.id, from: x.fromRole || "user", to: x.to, text: x.text, at: tsMs(x.at), delivered: x.deliveredTo.length });
  for (const { ev, at, seq } of events) {
    if (ev.kind !== TeamEventKind.MESSAGE_SENT) continue;
    const id = String(ev.data?.message_id ?? `ev_${seq}`);
    if (m.has(id)) continue;
    const delivered = ev.data?.delivered_to;
    m.set(id, {
      id,
      from: String(ev.data?.from_role ?? ev.role ?? "user") || "user",
      to: String(ev.data?.to ?? ""),
      text: ev.text,
      at,
      delivered: Array.isArray(delivered) ? delivered.length : 0,
    });
  }
  return [...m.values()].sort((a, b) => a.at - b.at);
}

export interface RunTotals {
  tasks: number;
  runs: number;
  active: number;
  queued: number;
  costMinor: number;
  currency: string;
  startedAt: number;
  endedAt: number;
}

/** Totals from the live tree (store), never below what the hub last reported. */
export function runTotals(run: TeamRun | undefined, tree: Task[], root: Task | undefined): RunTotals {
  let cost = 0;
  let active = 0;
  let queued = 0;
  for (const t of tree) {
    cost += costMinor(t.cost);
    if (t.id === root?.id) continue;
    if (t.phase === "waiting_slot" || (t.state === TaskState.QUEUED && t.phase !== "strategy")) queued++;
    else if (isActive(t.state)) active++;
  }
  const treeCost = Math.max(cost, costMinor(root?.treeCost), costMinor(run?.totalCost));
  const runsFromTasks = run ? run.tasks.reduce((n, t) => n + (t.runs || 0), 0) : 0;
  const extra = Math.max(0, tree.length - (run?.tasks.length ?? 0));
  return {
    tasks: Math.max(tree.length, run?.totalTasks ?? 0),
    runs: Math.max(run?.totalRuns ?? 0, runsFromTasks + extra, tree.filter((t) => t.state !== TaskState.QUEUED).length),
    active,
    queued,
    costMinor: treeCost,
    currency: root?.treeCost?.amount?.currency || run?.totalCost?.amount?.currency || "EUR",
    startedAt: tsMs(root?.createdAt),
    endedAt: root && !isActive(root.state) ? tsMs(root.updatedAt) : 0,
  };
}

export function limitsSummary(l: TeamLimits | undefined): Array<{ label: string; value: string }> {
  if (!l) return [];
  const out: Array<{ label: string; value: string }> = [];
  if (l.maxParallel) out.push({ label: "Parallel", value: String(l.maxParallel) });
  if (l.maxTasks) out.push({ label: "Tasks", value: String(l.maxTasks) });
  if (l.maxDepth) out.push({ label: "Depth", value: String(l.maxDepth) });
  if (l.maxRuns) out.push({ label: "Runs", value: String(l.maxRuns) });
  if (l.maxCost) out.push({ label: "Budget", value: `€${(Number(l.maxCost.minorUnits) / 100).toFixed(2)}` });
  if (l.timeoutSeconds) out.push({ label: "Timeout", value: humanSeconds(Number(l.timeoutSeconds)) });
  return out;
}

export function humanSeconds(s: number): string {
  if (s >= 3600) return `${Math.floor(s / 3600)}h${s % 3600 ? ` ${Math.round((s % 3600) / 60)}m` : ""}`;
  if (s >= 60) return `${Math.round(s / 60)}m`;
  return `${s}s`;
}

// ---- mutations ----------------------------------------------------------------------------------

export function useRunTeam() {
  const upsert = useEvents((s) => s.upsertTasks);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { teamId: string; prompt: string }) =>
      api().tasks.createTask({ prompt: v.prompt, teamId: v.teamId, source: "web", idempotencyKey: crypto.randomUUID() }),
    onSuccess: (res, v) => {
      if (res.task) upsert([res.task]);
      void qc.invalidateQueries({ queryKey: teamKeys.runs(v.teamId) });
      toast.success("Team run started");
    },
    onError: (err) => toast.error("Couldn't start the team", err),
  });
}

export function useSendTeamMessage(rootId: string) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: async (v: { to: string; text: string }) => api().teams.sendTeamMessage({ rootTaskId: rootId, to: v.to, text: v.text }),
    onSuccess: (res) => {
      // Show it at once; the MESSAGE_SENT event / refetch confirms it.
      qc.setQueryData<TeamRun | undefined>(teamKeys.run(rootId), (run) =>
        run && res.message && !run.messages.some((x) => x.id === res.message!.id) ? { ...run, messages: [...run.messages, res.message] } : run,
      );
    },
    onError: (err, v) => toast.error("Message not sent", err, { label: "Retry", onClick: () => m.mutate(v) }),
  });
  return m;
}
