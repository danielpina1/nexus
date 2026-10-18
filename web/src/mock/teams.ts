import { create, fromJson, type JsonValue } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { TeamEventKind } from "../gen/nexus/v1/event_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { ActionClass, CostSchema, Tier, TrustLevel } from "../gen/nexus/v1/common_pb";
import { ApprovalState } from "../gen/nexus/v1/approval_pb";
import {
  BlackboardEntrySchema,
  TeamMessageSchema,
  TeamRunSchema,
  TeamSchema,
  TeamStrategy,
  type Team,
  type TeamMessage,
  type TeamRun,
} from "../gen/nexus/v1/team_pb";
import { asString, asStringList, parseDurationSeconds, splitFrontmatter, type YValue } from "../lib/yaml";
import type { MockHub } from "./hub";
import { notes, upsertNote } from "./vaultData";
import { MIN, eur, newId, ts } from "./util";

const TERMINAL = new Set([TaskState.SUCCEEDED, TaskState.FAILED, TaskState.CANCELLED]);
const STRATEGIES: Record<string, TeamStrategy> = {
  hierarchical: TeamStrategy.HIERARCHICAL,
  parallel: TeamStrategy.PARALLEL,
  pipeline: TeamStrategy.PIPELINE,
  debate: TeamStrategy.DEBATE,
  auto: TeamStrategy.AUTO,
};
const TIERS: Record<string, Tier> = { simple: Tier.SIMPLE, medium: Tier.MEDIUM, heavy: Tier.HEAVY };
const obj = (v: YValue | undefined) => (v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, YValue>) : undefined);

/** Parses and validates a team note like teamdef + the catalog (docs/SWARMS.md §3). */
export function parseTeamMarkdown(markdown: string, agents: Map<string, { enabled: boolean }>): Team {
  const errors: string[] = [];
  const warnings: string[] = [];
  const { frontmatter: fm, hasFrontmatter, error } = splitFrontmatter(markdown);
  if (!hasFrontmatter) errors.push("missing YAML frontmatter (the note must start with ---)");
  if (error) errors.push(`frontmatter: ${error}`);
  const id = asString(fm.id);
  if (hasFrontmatter && !error) {
    if (asString(fm.type) !== "team") errors.push(`type must be "team" (got "${asString(fm.type) || "—"}")`);
    if (!id) errors.push("id is required");
    else if (!/^[a-z0-9][a-z0-9-]*$/.test(id)) errors.push("id must be lowercase letters, digits and dashes");
    if (!asString(fm.description)) errors.push("description is required (the router uses it)");
  }
  const stratName = asString(fm.strategy) || "hierarchical";
  const strategy = STRATEGIES[stratName];
  if (!strategy) errors.push(`strategy "${stratName}" must be hierarchical, parallel, pipeline, debate or auto`);

  let orchestratorAgentId = "";
  let inline: Team["inlineOrchestrator"];
  const o = fm.orchestrator;
  if (typeof o === "string" && o) orchestratorAgentId = o;
  else if (obj(o)) {
    const io = obj(o)!;
    inline = {
      instructions: asString(io.instructions),
      runtime: asString(io.runtime) || "auto",
      model: asString(io.model),
      tier: TIERS[asString(io.tier)] ?? Tier.UNSPECIFIED,
      tools: asStringList(io.tools),
      nodeTags: asStringList(obj(io.node)?.tags),
    } as Team["inlineOrchestrator"];
    if (!inline?.instructions) errors.push("orchestrator.instructions is required for an inline orchestrator");
  }
  if ((strategy === TeamStrategy.HIERARCHICAL || strategy === TeamStrategy.AUTO) && !orchestratorAgentId && !inline) {
    errors.push(`strategy ${stratName} needs an orchestrator (an agent id or inline instructions)`);
  }
  if (orchestratorAgentId) {
    const a = agents.get(orchestratorAgentId);
    if (!a) errors.push(`orchestrator: unknown agent "${orchestratorAgentId}"`);
    else if (!a.enabled) warnings.push(`orchestrator agent "${orchestratorAgentId}" is disabled`);
  }

  const raw = Array.isArray(fm.members) ? fm.members : [];
  const roles = new Set<string>();
  const members = raw.flatMap((m, i) => {
    const mm = obj(m);
    if (!mm) {
      errors.push(`members[${i}]: expected { agent, role, count, when }`);
      return [];
    }
    const agentId = asString(mm.agent);
    const role = asString(mm.role) || agentId;
    const count = Number(mm.count ?? 1) || 1;
    if (!agentId) errors.push(`members[${i}]: agent is required`);
    else if (!agents.has(agentId)) errors.push(`members[${i}]: unknown agent "${agentId}"`);
    else if (!agents.get(agentId)!.enabled) warnings.push(`member "${role}": agent "${agentId}" is disabled`);
    if (role === "orchestrator") errors.push(`members[${i}]: role "orchestrator" is reserved`);
    if (roles.has(role)) errors.push(`members[${i}]: duplicate role "${role}"`);
    roles.add(role);
    if (count < 1 || count > 20) errors.push(`members[${i}]: count must be 1–20`);
    return [{ agentId, role, count, when: asString(mm.when) }];
  });
  if ((strategy === TeamStrategy.PARALLEL || strategy === TeamStrategy.PIPELINE || strategy === TeamStrategy.DEBATE) && members.length === 0) {
    errors.push(`strategy ${stratName} needs members`);
  }
  if (strategy === TeamStrategy.DEBATE) {
    const debaters = members.filter((m) => m.role !== "judge").reduce((n, m) => n + m.count, 0);
    if (debaters < 2) errors.push("a debate needs at least 2 debaters (members other than role: judge)");
  }
  const lim = obj(fm.limits) ?? {};
  const shared = obj(fm.shared);
  return create(TeamSchema, {
    id,
    name: asString(fm.name) || id,
    description: asString(fm.description),
    icon: asString(fm.icon),
    orchestratorAgentId,
    inlineOrchestrator: inline,
    members,
    strategy: strategy ?? TeamStrategy.UNSPECIFIED,
    blackboard: shared?.blackboard !== false,
    limits: {
      maxParallel: Number(lim.max_parallel ?? 0) || 0,
      maxTasks: Number(lim.max_tasks ?? 0) || 0,
      maxDepth: Number(lim.max_depth ?? 0) || 0,
      maxCost: lim.max_cost_eur != null ? eur(Number(lim.max_cost_eur) * 100) : undefined,
      timeoutSeconds: BigInt(parseDurationSeconds(lim.timeout)),
      maxRuns: Number(lim.max_runs ?? 0) || 0,
    },
    bundleApprovals: obj(fm.approvals)?.bundle === true,
    debateRounds: Number(obj(fm.debate)?.rounds ?? 0) || (strategy === TeamStrategy.DEBATE ? 2 : 0),
    note: { id: `teams/${id}`, path: `teams/${id}.md`, title: asString(fm.name) || id },
    version: Number(fm.version ?? 1) || 1,
    enabled: errors.length === 0 && fm.enabled !== false,
    errors,
    warnings,
    triggers: asStringList(fm.triggers),
  });
}

// ---- run simulation -----------------------------------------------------------------------------

interface Step {
  at: number; // seconds after the start
  run: (t: number) => void;
  /** The step (and everything after it) waits until this is true. */
  gate?: () => boolean;
}

interface BoardRec {
  value: JsonValue;
  role: string;
  taskId: string;
  version: number;
  at: number;
}

interface RunState {
  rootId: string;
  team: Team;
  start: number;
  steps: Step[];
  idx: number;
  pausedSince?: number;
  done: boolean;
  board: Map<string, BoardRec>;
  messages: TeamMessage[];
  stoppedReason: string;
  runs: Map<string, number>;
  live: boolean;
}

const RT: Record<string, { runtime: string; model: string; node: string; cents: number }> = {
  "research-lead": { runtime: "claude-code", model: "claude-sonnet", node: "mac-studio", cents: 9 },
  researcher: { runtime: "openai", model: "smart", node: "vps", cents: 4 },
  "fact-checker": { runtime: "openai", model: "smart", node: "vps", cents: 3 },
  architect: { runtime: "claude-code", model: "claude-sonnet", node: "mac-studio", cents: 12 },
  "code-fixer": { runtime: "claude-code", model: "claude-sonnet", node: "mac-studio", cents: 14 },
  tester: { runtime: "codex", model: "gpt-5-codex", node: "mac-studio", cents: 6 },
  reviewer: { runtime: "claude-code", model: "claude-sonnet", node: "mac-studio", cents: 7 },
  "anthrex-dev": { runtime: "anthrex", model: "claude-sonnet", node: "mac-studio", cents: 18 },
  analyst: { runtime: "openai", model: "smart", node: "vps", cents: 3 },
  judge: { runtime: "openai", model: "best", node: "vps", cents: 6 },
  "product-researcher": { runtime: "openai", model: "smart", node: "vps", cents: 4 },
  critic: { runtime: "ollama", model: "fast-local", node: "gpu-box", cents: 0 },
};
const rtOf = (agentId: string) => RT[agentId] ?? { runtime: "openai", model: "smart", node: "vps", cents: 3 };

export class TeamsMock {
  markdown = new Map<string, string>();
  private runs = new Map<string, RunState>();
  private liveLoop = 0;

  constructor(private hub: MockHub) {
    for (const n of notes) if (n.path.startsWith("teams/") && n.type === "team" && !n.path.startsWith("teams/runs/")) this.markdown.set(String(n.frontmatter.id || n.id.slice(6)), n.raw);
  }

  private agentIndex() {
    return new Map(this.hub.listAgents().map((a) => [a.id, { enabled: a.enabled }]));
  }

  list(): Team[] {
    const idx = this.agentIndex();
    return [...this.markdown.values()].map((md) => parseTeamMarkdown(md, idx)).sort((a, b) => a.name.localeCompare(b.name));
  }

  get(id: string): { team: Team; markdown: string } {
    const md = this.markdown.get(id);
    if (!md) throw new ConnectError(`team ${id} not found`, Code.NotFound);
    return { team: parseTeamMarkdown(md, this.agentIndex()), markdown: md };
  }

  upsert(markdown: string, validateOnly: boolean): Team {
    const team = parseTeamMarkdown(markdown, this.agentIndex());
    if (validateOnly) return team;
    if (team.errors.length) throw new ConnectError(team.errors.join("\n"), Code.InvalidArgument);
    const prev = this.markdown.get(team.id);
    let md = markdown;
    if (prev) {
      const pv = Number(splitFrontmatter(prev).frontmatter.version ?? 1) || 1;
      if (team.version <= pv) md = markdown.replace(/^version:\s*\d+\s*$/m, `version: ${pv + 1}`);
    }
    this.markdown.set(team.id, md);
    upsertNote(`teams/${team.id}.md`, md);
    return parseTeamMarkdown(md, this.agentIndex());
  }

  // ---- runs -----------------------------------------------------------------------------------

  /** Seeds history (finished runs) and one live research-squad run that is ~half way through. */
  seed(now: number) {
    const past: Array<[string, string, number, "ok" | "budget" | "cancel"]> = [
      ["research-squad", "Deep dive: which e-bike for Porto's hills under €2,500?", 2 * 86_400_000 + 3 * 3_600_000, "ok"],
      ["debate-panel", "Should we move the home server to a NAS?", 26 * 3_600_000, "ok"],
      ["research-squad", "Full report on residential solar + battery payback in Portugal", 5 * 86_400_000, "budget"],
      ["dev-crew", "Implement FTS5 trigram search in the vault with tests", 3 * 86_400_000, "ok"],
      ["product-buyer", "Get me the best robot vacuum under €400", 4 * 86_400_000, "cancel"],
    ];
    for (const [teamId, prompt, ago, outcome] of past) this.start(teamId, prompt, { at: now - ago, instant: true, outcome });
    this.start("research-squad", "Deep dive: is a heat pump worth it for our flat in Porto?", { at: now - 26_000 });
  }

  start(teamId: string, prompt: string, opts: { at?: number; instant?: boolean; outcome?: "ok" | "budget" | "cancel" } = {}): Task {
    const { team } = this.get(teamId);
    if (team.errors.length) throw new ConnectError(`team ${teamId} has errors: ${team.errors.join("; ")}`, Code.FailedPrecondition);
    const h = this.hub;
    const t0 = opts.at ?? Date.now();
    const orch = team.orchestratorAgentId || (team.inlineOrchestrator?.instructions ? "eph:orchestrator" : "");
    const root = h.newTask(
      { title: titleOf(prompt), prompt, agentId: orch, teamId, role: "orchestrator", depth: 0, tier: Tier.HEAVY, source: "web", phase: orch ? "" : "strategy" },
      t0,
    );
    const st: RunState = {
      rootId: root.id,
      team,
      start: t0,
      steps: [],
      idx: 0,
      done: false,
      board: new Map(),
      messages: [],
      stoppedReason: "",
      runs: new Map([[root.id, 1]]),
      live: !opts.instant,
    };
    this.runs.set(root.id, st);
    st.steps = teamId === "research-squad" ? this.researchScript(st, prompt, opts.outcome ?? "ok") : this.genericScript(st, prompt, opts.outcome ?? "ok");
    if (opts.instant) {
      // Replay the whole script in the past (gates resolve themselves).
      for (const s of st.steps) s.run(t0 + s.at * 1000);
      st.idx = st.steps.length;
      st.done = true;
    } else {
      this.advance(st, Date.now());
    }
    return root;
  }

  tick() {
    const now = Date.now();
    let live = 0;
    for (const st of this.runs.values()) {
      if (st.done) continue;
      const root = this.hub.tasks.get(st.rootId);
      if (!root || TERMINAL.has(root.state)) {
        // Cancelled from the dashboard (or the kill switch).
        st.done = true;
        st.stoppedReason ||= "cancelled by owner";
        this.team(st, TeamEventKind.FINISHED, root, "Cancelled by owner", now, { state: "TASK_STATE_CANCELLED" });
        continue;
      }
      if (st.live) live++;
      this.advance(st, now);
    }
    // Keep a live run going in demo mode: start another one a little after the last ends.
    if (live === 0) {
      this.liveLoop++;
      if (this.liveLoop > 25) {
        this.liveLoop = 0;
        const prompts = [
          "Deep dive: is a heat pump worth it for our flat in Porto?",
          "Full report on the best mesh Wi-Fi for a 3-floor house",
          "Deep dive: should we switch electricity supplier this winter?",
        ];
        this.start("research-squad", prompts[Math.floor(Math.random() * prompts.length)]);
      }
    } else this.liveLoop = 0;
  }

  private advance(st: RunState, now: number) {
    while (st.idx < st.steps.length) {
      const s = st.steps[st.idx];
      if (s.gate && !s.gate()) {
        st.pausedSince ??= now;
        return;
      }
      if (st.pausedSince != null) {
        st.start += now - st.pausedSince;
        st.pausedSince = undefined;
      }
      const at = st.start + s.at * 1000;
      if (at > now) return;
      const root = this.hub.tasks.get(st.rootId);
      if (!root || TERMINAL.has(root.state)) return;
      st.idx++;
      s.run(Math.min(at, now));
    }
    st.done = true;
  }

  // ---- event helpers --------------------------------------------------------------------------

  private team(st: RunState, kind: TeamEventKind, subject: Task | undefined, text: string, at: number, data: Record<string, JsonValue> = {}, key = "") {
    this.hub.emit(
      st.rootId,
      {
        case: "team",
        value: { kind, teamId: st.team.id, rootTaskId: st.rootId, taskId: subject?.id ?? "", role: subject?.role ?? "", agentId: subject?.agentId ?? "", text, key, data: data as never },
      },
      { at, actor: "hub" },
    );
  }

  private member(st: RunState, parent: Task, agentId: string, role: string, title: string, at: number, queued = false): Task {
    const h = this.hub;
    const t = h.newTask(
      { title, prompt: title, agentId, parentId: parent.id, teamId: st.team.id, role, depth: (parent.depth ?? 0) + 1, tier: Tier.MEDIUM, source: "team", phase: queued ? "waiting_slot" : "" },
      at,
    );
    st.runs.set(t.id, 0);
    this.team(st, TeamEventKind.DELEGATED, t, title, at, { parent_id: parent.id, state: "TASK_STATE_QUEUED", phase: queued ? "waiting_slot" : "" });
    if (!queued) this.startTask(st, t, at + 900);
    return t;
  }

  private startTask(st: RunState, t: Task, at: number) {
    const r = rtOf(t.agentId);
    st.runs.set(t.id, (st.runs.get(t.id) ?? 0) + 1);
    this.hub.setTask(t.id, { state: TaskState.RUNNING, nodeId: r.node, phase: "" }, at);
  }

  private spend(st: RunState, t: Task, cents: number, at: number) {
    const r = rtOf(t.agentId);
    this.hub.cost(t.id, { cents, inTok: 2000 + cents * 900, outTok: 200 + cents * 60, model: r.model, runtime: r.runtime }, at);
    // Roll the tree's cost up to the root (Task.tree_cost), like the hub does.
    let total = 0;
    for (const x of this.hub.tasks.values()) if (x.id === st.rootId || x.rootId === st.rootId) total += Number(x.cost?.amount?.minorUnits ?? 0n);
    this.hub.setTask(st.rootId, { treeCost: create(CostSchema, { amount: eur(total) }) }, at);
  }

  private finish(st: RunState, t: Task, summary: string, at: number, state = TaskState.SUCCEEDED) {
    this.hub.out(t.id, summary + "\n", at, true);
    this.hub.setTask(t.id, { state, summary, phase: "" }, at);
    this.team(st, TeamEventKind.MEMBER_FINISHED, t, summary, at, { state: TaskState[state] ? `TASK_STATE_${TaskState[state]}` : "" });
  }

  private write(st: RunState, t: Task, key: string, value: JsonValue, at: number, append = false) {
    const cur = st.board.get(key);
    let v = value;
    if (append && cur) v = Array.isArray(cur.value) ? [...cur.value, value] : `${String(cur.value)}\n${String(value)}`;
    else if (append) v = [value];
    const version = (cur?.version ?? 0) + 1;
    st.board.set(key, { value: v, role: t.role || "orchestrator", taskId: t.id, version, at });
    const text = JSON.stringify(v);
    this.team(st, TeamEventKind.BLACKBOARD_UPDATED, t, text.length > 300 ? text.slice(0, 300) + "…" : text, at, { key, version, append }, key);
  }

  message(st: RunState, from: Task | undefined, fromRole: string, to: string, text: string, at: number): TeamMessage {
    const delivered = [...this.hub.tasks.values()]
      .filter((x) => (x.id === st.rootId || x.rootId === st.rootId) && x.state === TaskState.RUNNING && (to === "all" || x.role === to || x.id === to || (to === "orchestrator" && x.id === st.rootId)))
      .map((x) => x.id);
    const m = create(TeamMessageSchema, { id: newId("msg", at), fromTaskId: from?.id ?? "", fromRole, to, text, deliveredTo: delivered, at: ts(at) });
    st.messages.push(m);
    this.team(st, TeamEventKind.MESSAGE_SENT, from, text, at, { to, from_role: fromRole, delivered_to: delivered, message_id: m.id });
    return m;
  }

  // ---- scripts --------------------------------------------------------------------------------

  private researchScript(st: RunState, prompt: string, outcome: "ok" | "budget" | "cancel"): Step[] {
    const h = this.hub;
    const root = () => h.tasks.get(st.rootId)!;
    const topic = prompt.replace(/^(deep dive|full report on)[:\s]*/i, "");
    const qs = heatPump(topic);
    const kids: Task[] = [];
    let approvalId = "";
    const steps: Step[] = [
      {
        at: 0.5,
        run: (t) => {
          this.team(st, TeamEventKind.STARTED, root(), st.team.name, t, { strategy: "hierarchical", members: st.team.members.map((m) => m.role) });
          this.startTask(st, root(), t);
          h.out(st.rootId, `Planning the research on “${topic}”.\n`, t);
        },
      },
      {
        at: 2,
        run: (t) => {
          const c = h.tool(st.rootId, "memory.search", { query: topic }, ActionClass.READ, t);
          h.toolResult(st.rootId, c, { hits: 2, notes: ["Home energy", "Flat — Porto"] }, 230, t + 300);
          this.spend(st, root(), 6, t + 400);
        },
      },
      { at: 4, run: (t) => this.write(st, root(), "outline", qs.outline, t) },
      {
        at: 6,
        run: (t) => {
          for (let i = 0; i < 3; i++) kids.push(this.member(st, root(), "researcher", "researcher", qs.sub[i], t + i * 150));
          kids.push(this.member(st, root(), "researcher", "researcher", qs.sub[3], t + 600, true));
          h.tool(st.rootId, "team.delegate_many", { items: qs.sub.length }, ActionClass.COMPUTE, t);
        },
      },
      { at: 7, run: (t) => this.team(st, TeamEventKind.AWAITING, root(), "waiting for 4 task(s)", t, { task_ids: kids.map((k) => k.id) }) },
      {
        at: 9,
        run: (t) => {
          for (const k of kids.slice(0, 3)) {
            const c = h.tool(k.id, "web.search", { query: k.title }, ActionClass.READ, t);
            h.toolResult(k.id, c, { results: 8 }, 900, t + 900);
            this.spend(st, k, 3, t + 1000);
          }
        },
      },
      { at: 12, run: (t) => this.write(st, kids[0], "findings", qs.findings[0], t, true) },
      {
        at: 15,
        run: (t) => {
          this.spend(st, kids[1], 5, t);
          this.message(st, kids[1], "researcher", "orchestrator", qs.msg1, t);
        },
      },
      {
        at: 19,
        run: (t) => {
          this.write(st, kids[1], "findings", qs.findings[1], t, true);
          this.finish(st, kids[1], qs.done[1], t + 500);
          // A slot is free: the queued sub-question starts.
          this.startTask(st, kids[3], t + 900);
        },
      },
      {
        at: 23,
        run: (t) => {
          h.out(kids[2].id, "The Statista dataset needs a signed-in session.\n", t);
          const a = h.approval(
            kids[2].id,
            {
              cls: ActionClass.WRITE_EXTERNAL,
              level: TrustLevel.APPROVE,
              title: "Sign in to statista.com with the saved account",
              summary: "Researcher wants to open the “Heat pump noise levels by model” dataset (needs your saved login; read-only).",
              payload: { site: "statista.com", account: "daniel@…", purpose: "read one dataset" },
              expiresIn: 30 * MIN,
            },
            t,
          );
          approvalId = a.id;
          h.setTask(kids[2].id, { state: TaskState.WAITING_APPROVAL }, t);
          this.team(st, TeamEventKind.APPROVAL_PENDING, kids[2], a.title, t, { approval_id: a.id });
          if (st.live) {
            // Demo: approved from the iPhone if nobody decides here within ~70 s.
            setTimeout(() => {
              if (h.approvals.get(a.id)?.state === ApprovalState.PENDING) h.decideInternal(a.id, true, "device:dev_iphone");
            }, 70_000);
          } else {
            h.decideInternal(a.id, outcome !== "cancel", "device:dev_iphone");
          }
        },
      },
      {
        at: 27,
        run: (t) => {
          this.spend(st, kids[0], 4, t);
          this.write(st, kids[0], "findings", qs.findings[2], t + 200, true);
        },
      },
      { at: 31, run: (t) => this.finish(st, kids[0], qs.done[0], t) },
      {
        at: 35,
        run: (t) => {
          this.spend(st, kids[3], 4, t);
          this.message(st, kids[3], "researcher", "orchestrator", qs.msg2, t + 300);
        },
      },
      {
        at: 42,
        run: (t) => {
          this.write(st, kids[3], "findings", qs.findings[3], t, true);
          this.finish(st, kids[3], qs.done[3], t + 400);
        },
      },
    ];
    if (outcome === "cancel") {
      steps.push({ at: 45, run: (t) => h.setTask(st.rootId, { state: TaskState.CANCELLED, summary: "Cancelled by owner." }, t) });
      return steps;
    }
    steps.push(
      {
        at: 46,
        gate: () => !approvalId || h.approvals.get(approvalId)?.state !== ApprovalState.PENDING,
        run: (t) => {
          const ok = h.approvals.get(approvalId)?.state === ApprovalState.APPROVED;
          h.setTask(kids[2].id, { state: TaskState.RUNNING }, t);
          st.runs.set(kids[2].id, (st.runs.get(kids[2].id) ?? 1) + 1);
          h.out(kids[2].id, ok ? "Signed in; reading the dataset.\n" : "No login: using the manufacturer's spec sheets instead.\n", t);
          this.spend(st, kids[2], 5, t + 500);
        },
      },
      {
        at: 52,
        run: (t) => {
          this.write(st, kids[2], "findings", qs.findings[4], t, true);
          this.finish(st, kids[2], qs.done[2], t + 400);
        },
      },
    );
    if (outcome === "budget") {
      steps.push({
        at: 55,
        run: (t) => {
          this.spend(st, root(), 160, t);
          const why = "tree budget of €3.00 reached (€3.07 spent)";
          st.stoppedReason = why;
          this.team(st, TeamEventKind.LIMIT_REACHED, root(), `budget exceeded: ${why}`, t);
          h.setTask(st.rootId, { state: TaskState.FAILED, summary: "Budget exceeded before the report was fact-checked." }, t + 200);
          this.team(st, TeamEventKind.FINISHED, root(), "Budget exceeded", t + 300, { state: "TASK_STATE_FAILED" });
        },
      });
      return steps;
    }
    let checker: Task | undefined;
    steps.push(
      {
        at: 56,
        run: (t) => {
          this.spend(st, root(), 14, t);
          this.write(st, root(), "draft", qs.draft, t + 300);
          checker = this.member(st, root(), "fact-checker", "fact-checker", "Fact-check the draft report and its sources", t + 600);
        },
      },
      {
        at: 61,
        run: (t) => {
          this.spend(st, checker!, 4, t);
          this.message(st, checker, "fact-checker", "orchestrator", qs.check, t + 200);
        },
      },
      { at: 64, run: (t) => this.finish(st, checker!, "2 claims needed sources; both fixed in the draft notes.", t) },
      {
        at: 67,
        run: (t) => {
          this.spend(st, root(), 9, t);
          this.team(st, TeamEventKind.REPORTED, root(), "Final report saved to research/heat-pump-porto.md", t);
          h.out(st.rootId, qs.final, t + 200, true);
          h.setTask(st.rootId, { state: TaskState.SUCCEEDED, summary: qs.summary }, t + 400);
          this.team(st, TeamEventKind.FINISHED, root(), qs.summary, t + 500, { state: "TASK_STATE_SUCCEEDED" });
          st.stoppedReason = "team run ended (succeeded)";
        },
      },
    );
    return steps;
  }

  private genericScript(st: RunState, prompt: string, outcome: "ok" | "budget" | "cancel"): Step[] {
    const h = this.hub;
    const team = st.team;
    const root = () => h.tasks.get(st.rootId)!;
    const debate = team.strategy === TeamStrategy.DEBATE;
    const roster = team.members.flatMap((m) => Array.from({ length: Math.min(m.count || 1, 2) }, (_, i) => ({ ...m, idx: i })));
    const workers = debate ? roster.filter((m) => m.role !== "judge") : roster;
    const judge = roster.find((m) => m.role === "judge");
    const steps: Step[] = [
      {
        at: 0.5,
        run: (t) => {
          this.team(st, TeamEventKind.STARTED, root(), team.name, t, { strategy: TeamStrategy[team.strategy].toLowerCase(), members: team.members.map((m) => m.role) });
          if (!debate) this.startTask(st, root(), t);
          else this.team(st, TeamEventKind.STRATEGY_STEP, root(), `debate: round 1 of ${team.debateRounds || 2}`, t + 100);
        },
      },
      { at: 2, run: (t) => this.write(st, root(), debate ? "question" : "plan", debate ? prompt : `1. ${workers.map((w) => w.role).join("\n2. ")}`, t) },
    ];
    const kids: Task[] = [];
    let off = 4;
    for (const [i, w] of workers.entries()) {
      steps.push({
        at: off + i * 1.2,
        run: (t) => kids.push(this.member(st, root(), w.agentId, w.role, `${w.when || w.role}${w.idx ? ` (${w.idx + 1})` : ""}`.replace(/^./, (c) => c.toUpperCase()).slice(0, 110), t)),
      });
    }
    off += workers.length * 1.2 + 6;
    for (const [i] of workers.entries()) {
      steps.push({
        at: off + i * 3,
        run: (t) => {
          const k = kids[i];
          this.spend(st, k, rtOf(k.agentId).cents || 1, t);
          this.write(st, k, debate ? `position:${k.role}` : k.role, `${k.role}: ${debate ? "argued its position with 3 points" : "done — see the result"}`, t + 200);
          this.finish(st, k, debate ? `Position of the ${k.role} (round ${team.debateRounds || 2})` : `${k.role} finished its part`, t + 400);
        },
      });
    }
    off += workers.length * 3 + 2;
    if (outcome === "cancel") {
      steps.push({ at: off, run: (t) => h.setTask(st.rootId, { state: TaskState.CANCELLED, summary: "Cancelled by owner." }, t) });
      return steps;
    }
    if (judge) {
      let j: Task | undefined;
      steps.push(
        {
          at: off,
          run: (t) => {
            this.team(st, TeamEventKind.STRATEGY_STEP, root(), "debate finished; the judge decides", t);
            j = this.member(st, root(), judge.agentId, "judge", "Weigh the final positions and give a verdict", t + 200);
          },
        },
        { at: off + 5, run: (t) => this.finish(st, j!, "Verdict: yes, with a 6-month trial (2 of 3 arguments held).", t) },
      );
      off += 6;
    }
    steps.push({
      at: off + 1,
      run: (t) => {
        this.spend(st, root(), 8, t);
        const summary = debate ? "Verdict: yes — move it, but keep the old server as a cold backup for 6 months." : `Done: ${titleOf(prompt)}`;
        h.out(st.rootId, summary, t + 100, true);
        h.setTask(st.rootId, { state: TaskState.SUCCEEDED, summary, phase: "" }, t + 300);
        this.team(st, TeamEventKind.FINISHED, root(), summary, t + 400, { state: "TASK_STATE_SUCCEEDED" });
        st.stoppedReason = "team run ended (succeeded)";
      },
    });
    return steps;
  }

  // ---- service ops ----------------------------------------------------------------------------

  getRun(rootId: string): TeamRun {
    const st = this.runs.get(rootId);
    const root = this.hub.tasks.get(rootId);
    if (!root) throw new ConnectError(`task ${rootId} not found`, Code.NotFound);
    const tree = [...this.hub.tasks.values()].filter((t) => t.id === rootId || t.rootId === rootId).sort((a, b) => (a.id < b.id ? -1 : 1));
    let cost = 0;
    let active = 0;
    let queued = 0;
    for (const t of tree) {
      cost += Number(t.cost?.amount?.minorUnits ?? 0n);
      if (t.id === rootId) continue;
      if (t.phase === "waiting_slot") queued++;
      else if (!TERMINAL.has(t.state)) active++;
    }
    const team = st?.team ?? (root.teamId ? this.get(root.teamId).team : undefined);
    const timeout = Number(team?.limits?.timeoutSeconds ?? 0n);
    return create(TeamRunSchema, {
      root,
      team,
      tasks: tree.map((t) => ({ task: t, runs: st?.runs.get(t.id) ?? 1, ephemeral: t.agentId.startsWith("eph:"), memberRole: t.id === rootId ? "" : t.role })),
      blackboard: [...(st?.board.entries() ?? [])].map(([key, b]) =>
        create(BlackboardEntrySchema, { key, value: fromJson(ValueSchema, b.value), authorTaskId: b.taskId, authorRole: b.role, version: b.version, updatedAt: ts(b.at) }),
      ),
      messages: st?.messages ?? [],
      totalCost: { amount: eur(cost), model: "", runtime: "" },
      totalTasks: tree.length,
      totalRuns: tree.reduce((n, t) => n + (st?.runs.get(t.id) ?? 1), 0),
      activeTasks: active,
      queuedTasks: queued,
      limits: team?.limits,
      stoppedReason: st?.stoppedReason && !st.stoppedReason.startsWith("team run ended") ? st.stoppedReason : TERMINAL.has(root.state) ? (st?.stoppedReason ?? "") : "",
      deadline: timeout ? ts((st?.start ?? Number(root.createdAt?.seconds ?? 0n) * 1000) + timeout * 1000) : undefined,
    });
  }

  sendMessage(rootId: string, to: string, text: string): TeamMessage {
    const st = this.runs.get(rootId);
    if (!st) throw new ConnectError("not a team run", Code.NotFound);
    const root = this.hub.tasks.get(rootId);
    if (!root || TERMINAL.has(root.state)) throw new ConnectError("the team run has ended", Code.FailedPrecondition);
    const m = this.message(st, undefined, "user", to || "orchestrator", text, Date.now());
    setTimeout(() => {
      const r = this.hub.tasks.get(rootId);
      if (r && !TERMINAL.has(r.state)) this.message(st, r, "orchestrator", "all", `Noted from the user: “${text.slice(0, 120)}” — adjusting the plan.`, Date.now());
    }, 2500);
    return m;
  }
}

function titleOf(prompt: string) {
  const t = prompt.trim().replace(/\s+/g, " ");
  return t.length > 80 ? t.slice(0, 78) + "…" : t;
}

function heatPump(topic: string) {
  return {
    outline: `# ${topic}\n1. Upfront cost and subsidies (Fundo Ambiental)\n2. Efficiency in Porto's climate (SCOP)\n3. Noise and installation rules for flats\n4. Running cost vs. the current gas boiler\n5. Recommendation`,
    sub: [
      "Upfront cost of an air-to-water heat pump for a 95 m² flat in Porto, and 2026 subsidies",
      "Real-world SCOP of air-to-water heat pumps in Porto's climate (mild, humid winters)",
      "Noise limits and condominium rules for outdoor units on Portuguese apartment balconies",
      "Running cost: heat pump vs. the current gas boiler at 2026 tariffs (EDP, Goldenergy)",
    ],
    findings: [
      "Installed price €6.5–9k for 8 kW units (Daikin Altherma 3, Panasonic Aquarea); Fundo Ambiental 2026 refunds 85 % up to €2,500. [1][2]",
      "Subsidy window opens in March; invoices must be dated after the application. [3]",
      "Average SCOP 3.8–4.3 at Porto design temps (≈3 °C); defrost cycles cost ~5 % in humid weeks. [4]",
      "Bi-hourly tariff makes the heat pump €38/month cheaper than gas for this flat (≈4,200 kWh/yr heat). [5]",
      "Outdoor units ≤ 45 dB(A) at 1 m are fine under DL 9/2007; condominium approval needed for façade units. [6]",
    ],
    msg1: "Subsidy: Fundo Ambiental 2026 refunds 85 % up to €2,500, but only for invoices after the application date — worth flagging early.",
    msg2: "Tariffs: on the bi-hourly EDP plan the heat pump costs ~€38/month less than gas in winter. Summer cooling adds ~€9/month if used.",
    done: [
      "Cost & subsidies: €6.5–9k installed, up to €2,500 back.",
      "Efficiency: SCOP ~4 in Porto; humid weeks −5 %.",
      "Noise: OK at ≤ 45 dB(A); needs condominium approval.",
      "Running cost: −€38/month vs gas on bi-hourly tariff.",
    ],
    draft: "Draft v1: Worth it. Net cost ≈ €5.2k after subsidy; saves ≈ €420/yr vs gas → payback ~12 years, faster if gas rises. Pick a ≤ 45 dB unit; get condominium approval first.",
    check: "Two claims lack a primary source: the 85 % refund cap (cite the Fundo Ambiental notice) and the defrost loss figure. Everything else checks out.",
    final: "## Recommendation\nYes — a heat pump is worth it for the flat, mainly for comfort and resilience to gas prices.\n\n- **Net cost:** ≈ €5.2k after the €2,500 Fundo Ambiental refund (apply *before* buying).\n- **Savings:** ≈ €420/yr vs gas on the bi-hourly tariff → payback ~12 years.\n- **Noise:** choose ≤ 45 dB(A); ask the condominium first.\n\nReport: research/heat-pump-porto.md",
    summary: "Worth it: ≈ €5.2k net, saves ≈ €420/yr (payback ~12 y). Apply for the subsidy before buying.",
  };
}
