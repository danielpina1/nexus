import { clone, create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { EventSchema, NoticeKind, type Event } from "../gen/nexus/v1/event_pb";
import { TaskSchema, TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { ApprovalSchema, ApprovalState, type Approval } from "../gen/nexus/v1/approval_pb";
import { ActionClass, CostSchema, NoteRefSchema, Tier, TrustLevel, type NoteRef } from "../gen/nexus/v1/common_pb";
import { CommandKind, type Route } from "../gen/nexus/v1/command_pb";
import { NodeStatusSchema, type Node } from "../gen/nexus/v1/node_pb";
import type { Device } from "../gen/nexus/v1/device_pb";
import { FactSchema, FactStatus, type Fact } from "../gen/nexus/v1/vault_pb";
import type { Agent } from "../gen/nexus/v1/agent_pb";
import { parseAgentMarkdown } from "../lib/agentParse";
import { makeDevices, makeNodes } from "./fixtures";
import { DAY, HOUR, MIN, eur, newId, prand, randomBytes, sleep, ts } from "./util";
import { notes, upsertNote, findNote } from "./vaultData";
import { cartSvg, diffText } from "./artifacts";

type EventInit = MessageInitShape<typeof EventSchema>;
type Body = EventInit["body"];

const TERMINAL = new Set([TaskState.SUCCEEDED, TaskState.FAILED, TaskState.CANCELLED]);
const enc = new TextEncoder();

interface CostSpec {
  cents: number;
  inTok: number;
  outTok: number;
  model: string;
  runtime: string;
}

const RT: Record<string, { model: string; runtime: string; node: string; tier: Tier }> = {
  assistant: { model: "smart", runtime: "openai", node: "vps", tier: Tier.MEDIUM },
  "code-fixer": { model: "claude-sonnet", runtime: "claude-code", node: "mac-studio", tier: Tier.HEAVY },
  "grocery-continente": { model: "smart", runtime: "browser", node: "mac-studio", tier: Tier.MEDIUM },
  ops: { model: "smart", runtime: "openai", node: "vps", tier: Tier.MEDIUM },
  librarian: { model: "fast-local", runtime: "ollama", node: "gpu-box", tier: Tier.SIMPLE },
};

export const GROCERY_ITEMS: Array<[string, number, number]> = [
  ["Leite Mimosa meio-gordo 1L", 6, 534],
  ["Pão de forma Bimbo integral", 1, 229],
  ["Café Delta Lote Chávena 250g", 2, 798],
  ["Ovos classe M (12)", 1, 289],
  ["Bananas da Madeira (1kg)", 1, 189],
  ["Maçã Gala (1kg)", 1, 199],
  ["Iogurte natural sem lactose (4x)", 2, 398],
  ["Queijo flamengo fatiado", 1, 279],
  ["Fiambre da perna extra", 1, 319],
  ["Peito de frango (1kg)", 1, 699],
  ["Salmão fresco (posta)", 2, 1198],
  ["Arroz carolino 1kg", 1, 149],
  ["Massa esparguete Milaneza", 2, 198],
  ["Tomate chucha (1kg)", 1, 249],
  ["Alface iceberg", 1, 99],
  ["Cebola (1kg)", 1, 129],
  ["Azeite Gallo virgem extra 750ml", 1, 899],
  ["Detergente Fairy", 1, 349],
  ["Papel higiénico Renova (12)", 1, 599],
  ["Água Luso 1.5L (6x)", 2, 498],
  ["Chocolate negro Côte d'Or", 2, 398],
  ["Cerveja Super Bock (6x)", 1, 499],
];

const CAPTURES = [
  ["inbox/2026-10-11 voice memo.md", "voice memo about the Home Assistant integration", "[[nexus]]"],
  ["inbox/link: SQLite FTS5 trigram.md", "link on SQLite FTS5 trigram tokenizer", "[[nexus]]"],
  ["inbox/receipt pharmacy.md", "scanned pharmacy receipt", "[[Ana Pina]]"],
  ["inbox/idea weekend trip.md", "idea: weekend trip to Gerês", "[[Ana Pina]]"],
  ["episodes/2026-10-10 nightly check.md", "ops nightly check episode", "[[vps]]"],
  ["inbox/screenshot Continente promo.md", "screenshot of a Continente coffee promo", "[[Café Delta Lote Chávena]]"],
  ["inbox/call notes Rui.md", "call notes with Rui", "[[Rui Costa]]"],
  ["inbox/article local LLM routing.md", "article on local LLM routing", "[[gpu-box]]"],
  ["inbox/book recommendation.md", "book recommendation from Ana", "[[Ana Pina]]"],
  ["inbox/todo renew domain.md", "todo: renew domain", "[[vps]]"],
  ["inbox/photo whiteboard.md", "whiteboard photo from planning", "[[nexus]]"],
  ["inbox/podcast notes.md", "podcast notes on agent evals", "[[nexus]]"],
  ["inbox/gpu prices.md", "GPU price comparison", "[[gpu-box]]"],
  ["inbox/recipe.md", "salmon recipe for Sunday", "[[Shopping preferences]]"],
];

export class MockHub {
  seq = 0n;
  log: Event[] = [];
  private waiters = new Set<() => void>();
  tasks = new Map<string, Task>();
  approvals = new Map<string, Approval>();
  nodes: Node[];
  devices: Device[];
  agentMarkdown = new Map<string, string>();
  facts: Fact[] = [];
  private onDecision = new Map<string, (approved: boolean) => void>();
  private onAnswer = new Map<string, (answer: string) => void>();
  private onMessage = new Map<string, (text: string) => void>();
  private tick = 0;
  private grocery = { taskId: "", parentId: "", item: 9, phase: 0, total: 0, done: false };
  private librarian = { taskId: "", i: 0, phase: 0, batch: 1, cooldown: 0 };

  constructor() {
    const now = Date.now();
    this.nodes = makeNodes(now);
    this.devices = makeDevices(now);
    for (const n of notes) if (n.type === "agent") this.agentMarkdown.set(asId(n), n.raw);
    this.buildHistory(now);
    this.buildCurrent(now);
    setInterval(() => this.onTick(), 1000);
  }

  // ---- event log ------------------------------------------------------------------------------

  emit(taskId: string, body: Body, opts: { at?: number; actor?: string; runId?: string } = {}): Event {
    this.seq++;
    const task = taskId ? this.tasks.get(taskId) : undefined;
    const ev = create(EventSchema, {
      seq: this.seq,
      at: ts(opts.at ?? Date.now()),
      taskId,
      runId: opts.runId ?? task?.currentRunId ?? "",
      actor: opts.actor ?? (task?.agentId ? `agent:${task.agentId}` : "hub"),
      body,
    });
    this.log.push(ev);
    for (const w of this.waiters) w();
    return ev;
  }

  async *subscribe(sinceSeq: bigint, taskIds: string[], inboxOnly: boolean, signal: AbortSignal): AsyncGenerator<Event> {
    let cursor = Number(sinceSeq < 0n ? 0n : sinceSeq);
    if (cursor > this.log.length) cursor = 0; // client remembers a seq from a previous mock session
    const filter = new Set(taskIds);
    while (!signal.aborted) {
      while (cursor < this.log.length) {
        const ev = this.log[cursor++];
        if (filter.size && !filter.has(ev.taskId)) continue;
        if (inboxOnly && !["approval", "question", "notice"].includes(ev.body.case ?? "")) continue;
        yield ev;
      }
      await new Promise<void>((resolve) => {
        const done = () => {
          this.waiters.delete(done);
          signal.removeEventListener("abort", done);
          resolve();
        };
        this.waiters.add(done);
        signal.addEventListener("abort", done, { once: true });
      });
    }
  }

  // ---- task helpers ---------------------------------------------------------------------------

  newTask(init: { title: string; prompt?: string; agentId?: string; workflowId?: string; parentId?: string; tier?: Tier; source?: string }, at = Date.now()): Task {
    const id = newId("tsk", at);
    const parent = init.parentId ? this.tasks.get(init.parentId) : undefined;
    const t = create(TaskSchema, {
      state: TaskState.QUEUED,
      source: "web",
      ...init,
      id,
      rootId: parent ? parent.rootId || parent.id : id,
      createdAt: ts(at),
      updatedAt: ts(at),
      currentRunId: newId("run", at),
      cost: { amount: eur(0), model: "", runtime: "" },
    });
    this.tasks.set(id, t);
    this.emit(id, { case: "taskUpdated", value: { task: clone(TaskSchema, t) } }, { at, actor: "hub" });
    return t;
  }

  setTask(id: string, patch: Partial<Task>, at = Date.now(), actor?: string) {
    const t = this.tasks.get(id);
    if (!t) return;
    Object.assign(t, patch);
    t.updatedAt = ts(at);
    this.emit(id, { case: "taskUpdated", value: { task: clone(TaskSchema, t) } }, { at, actor: actor ?? "hub" });
  }

  out(id: string, text: string, at?: number, isFinal = false) {
    this.emit(id, { case: "runOutput", value: { text, isFinal } }, { at });
  }

  tool(id: string, tool: string, input: Record<string, unknown>, cls: ActionClass, at?: number): string {
    const callId = newId("call", at);
    this.emit(id, { case: "toolCall", value: { callId, tool, input: input as never, actionClass: cls } }, { at });
    return callId;
  }

  toolResult(id: string, callId: string, output: Record<string, unknown>, ms: number, at?: number, error = "") {
    this.emit(id, { case: "toolResult", value: { callId, output: output as never, error, durationMs: BigInt(ms) } }, { at });
  }

  cost(id: string, c: CostSpec, at?: number) {
    const t = this.tasks.get(id);
    this.emit(
      id,
      {
        case: "cost",
        value: { cost: { amount: eur(c.cents), inputTokens: BigInt(c.inTok), outputTokens: BigInt(c.outTok), model: c.model, runtime: c.runtime } },
      },
      { at },
    );
    if (t) {
      const prev = t.cost;
      t.cost = create(CostSchema, {
        amount: eur(Number(prev?.amount?.minorUnits ?? 0n) + c.cents),
        inputTokens: (prev?.inputTokens ?? 0n) + BigInt(c.inTok),
        outputTokens: (prev?.outputTokens ?? 0n) + BigInt(c.outTok),
        model: c.model,
        runtime: c.runtime,
      });
    }
  }

  notice(taskId: string, kind: NoticeKind, title: string, body: string, opts: { at?: number; data?: Record<string, unknown>; notes?: Array<{ id: string; path: string; title: string }> } = {}) {
    const id = newId("ntc", opts.at);
    this.emit(
      taskId,
      { case: "notice", value: { id, kind, title, body, data: (opts.data ?? {}) as never, notes: opts.notes ?? [] } },
      { at: opts.at, actor: "hub" },
    );
    return id;
  }

  approval(
    taskId: string,
    init: { cls: ActionClass; level: TrustLevel; title: string; summary: string; payload: Record<string, unknown>; previews?: MessageInitShape<typeof ApprovalSchema>["previews"]; expiresIn?: number },
    at = Date.now(),
  ): Approval {
    const t = this.tasks.get(taskId)!;
    const a = create(ApprovalSchema, {
      id: newId("apr", at),
      taskId,
      runId: t.currentRunId,
      agentId: t.agentId,
      actionClass: init.cls,
      level: init.level,
      title: init.title,
      summary: init.summary,
      payload: init.payload as never,
      previews: init.previews ?? [],
      payloadHash: randomBytes(32),
      challenge: randomBytes(32),
      state: ApprovalState.PENDING,
      createdAt: ts(at),
      expiresAt: ts(at + (init.expiresIn ?? 2 * HOUR)),
      nodeId: t.nodeId,
    });
    this.approvals.set(a.id, a);
    this.emit(taskId, { case: "approval", value: clone(ApprovalSchema, a) }, { at });
    return a;
  }

  question(taskId: string, text: string, choices: string[], allowFreeText: boolean, at?: number): string {
    const questionId = newId("q", at);
    this.emit(taskId, { case: "question", value: { questionId, text, choices, allowFreeText } }, { at });
    return questionId;
  }

  private alive(id: string) {
    const t = this.tasks.get(id);
    return !!t && !TERMINAL.has(t.state);
  }

  private agentFor(id: string) {
    return RT[id] ?? RT.assistant;
  }

  private costFor(agentId: string, cents: number, inTok: number, outTok: number): CostSpec {
    const r = this.agentFor(agentId);
    return { cents, inTok, outTok, model: r.model, runtime: r.runtime };
  }

  // ---- history (last 14 days) -------------------------------------------------------------------

  private buildHistory(now: number) {
    const plan: Array<{ at: number; run: () => void }> = [];
    const at = (t: number, run: () => void) => plan.push({ at: t, run });
    const dayStart = (d: number) => {
      const x = new Date(now - d * DAY);
      x.setHours(0, 0, 0, 0);
      return x.getTime();
    };
    const asks = [
      "Draft a reply to the landlord about the boiler",
      "Summarize the Q3 invoices",
      "Plan meals for next week",
      "What did Rui say about Home Assistant?",
      "Compare GPU prices for a second 4090",
      "Find the warranty for the dishwasher",
      "Write the weekly status for nexus",
      "Translate the school letter to English",
      "Remind me what Ana wanted for her birthday",
    ];
    const fixes = [
      "Fix flaky event-stream test",
      "Add FTS5 trigram search to the vault",
      "Bump connect-go and regenerate",
      "Refactor policy engine tests",
      "Fix iOS share extension crash",
    ];
    let seed = 1;
    const r = () => prand(seed++);

    // run = a complete simple task in the past
    const pastTask = (o: { at: number; title: string; agentId: string; prompt?: string; workflowId?: string; source?: string; cents: number; inTok: number; outTok: number; output: string; failed?: string; durMin?: number; parentId?: string }) => {
      const rt = this.agentFor(o.agentId);
      let id = "";
      const dur = (o.durMin ?? 1) * MIN;
      at(o.at, () => {
        id = this.newTask(
          { title: o.title, prompt: o.prompt ?? o.title, agentId: o.agentId, workflowId: o.workflowId ?? "", tier: rt.tier, source: o.source ?? "ios-chat", parentId: o.parentId ?? "" },
          o.at,
        ).id;
      });
      at(o.at + 2000, () => this.setTask(id, { state: TaskState.RUNNING, nodeId: rt.node }, o.at + 2000));
      if (o.agentId === "assistant" || o.agentId === "ops") {
        at(o.at + 4000, () => {
          const c = this.tool(id, o.agentId === "ops" ? "node.exec" : "memory.search", o.agentId === "ops" ? { cmd: "uptime && df -h /" } : { query: o.title }, ActionClass.READ, o.at + 4000);
          this.toolResult(id, c, { hits: 3 }, 180, o.at + 4500);
        });
      }
      at(o.at + dur * 0.8, () => this.out(id, o.output, o.at + dur * 0.8, true));
      at(o.at + dur * 0.9, () => this.cost(id, this.costFor(o.agentId, o.cents, o.inTok, o.outTok), o.at + dur * 0.9));
      at(o.at + dur, () =>
        this.setTask(
          id,
          o.failed
            ? { state: TaskState.FAILED, summary: o.failed }
            : { state: TaskState.SUCCEEDED, summary: o.output.split("\n")[0].slice(0, 140) },
          o.at + dur,
        ),
      );
      return () => id;
    };

    for (let d = 13; d >= 0; d--) {
      const base = dayStart(d);
      const cutoff = now - 20 * MIN;
      const push = (o: Parameters<typeof pastTask>[0]) => {
        if (o.at + (o.durMin ?? 1) * MIN < cutoff) pastTask(o);
      };
      push({ at: base + 2 * HOUR + r() * 10 * MIN, title: "Nightly health check", agentId: "ops", source: "trigger", cents: 1 + Math.round(r() * 2), inTok: 3200, outTok: 400, output: "All machines healthy. vps load 0.12, backups verified (312 MB).", durMin: 2 });
      push({ at: base + 3 * HOUR + r() * 20 * MIN, title: `File ${3 + Math.round(r() * 9)} captures`, agentId: "librarian", source: "trigger", cents: 0, inTok: 30_000 + Math.round(r() * 50_000), outTok: 4000 + Math.round(r() * 6000), output: "Filed captures, linked 7 notes, proposed 3 facts.", durMin: 4 });
      push({ at: base + 7.5 * HOUR, title: "Morning brief", agentId: "assistant", workflowId: "morning-brief", source: "trigger", cents: 3 + Math.round(r() * 4), inTok: 9000, outTok: 900, output: "Brief ready: 2 meetings, groceries Saturday, gpu-box disk trending up.", durMin: 1 });
      const nAsk = 1 + Math.floor(r() * 3);
      for (let k = 0; k < nAsk; k++) {
        const title = asks[Math.floor(r() * asks.length)];
        push({ at: base + (10 + r() * 11) * HOUR, title, agentId: "assistant", cents: 2 + Math.round(r() * 14), inTok: 4000 + Math.round(r() * 12000), outTok: 500 + Math.round(r() * 1500), output: `Done: ${title.toLowerCase()}. Draft saved to the vault.`, durMin: 1 + r() * 2 });
      }
      if (r() > 0.45) {
        const title = fixes[Math.floor(r() * fixes.length)];
        const failed = d === 5 ? "Tests still failing after 3 attempts — needs a human look at the iOS signing setup." : undefined;
        push({ at: base + (14 + r() * 6) * HOUR, title, agentId: "code-fixer", cents: 40 + Math.round(r() * 180), inTok: 180_000 + Math.round(r() * 300_000), outTok: 12_000 + Math.round(r() * 20_000), output: failed ? "Could not make the tests pass." : `${title}: branch pushed, PR opened, CI green.`, failed, durMin: 12 + r() * 30, source: "web" });
      }
      if (d === 7) {
        push({ at: base + 10 * HOUR, title: "Weekly groceries", agentId: "grocery-continente", cents: 31, inTok: 61_000, outTok: 5200, output: "Ordered 21 items for €81.12, delivery Sat 9–11.", durMin: 14 });
      }
    }

    // A recent failure that surfaces as an alert.
    const failAt = now - 3 * HOUR;
    const fail = pastTask({ at: failAt, title: "Renew nexus.example domain", agentId: "assistant", cents: 6, inTok: 8000, outTok: 600, output: "Registrar asked for 2FA on login; could not continue.", failed: "Registrar login needs a 2FA code — I could not continue.", durMin: 3, source: "ios-chat" });
    at(failAt + 3 * MIN + 1000, () =>
      this.notice(fail(), NoticeKind.ALERT, "Domain renewal failed", "The registrar asked for a 2FA code. Renew manually or send me the code from your phone.", { at: failAt + 3 * MIN + 1000 }),
    );

    plan.sort((a, b) => a.at - b.at);
    for (const p of plan) p.run();
  }

  // ---- what is happening right now ----------------------------------------------------------------

  private buildCurrent(now: number) {
    // Morning brief notice + suggestion + fact review.
    const brief = [...this.tasks.values()].filter((t) => t.workflowId === "morning-brief").pop();
    this.notice(
      brief?.id ?? "",
      NoticeKind.BRIEF,
      "Good morning — Saturday brief",
      "3 agents running and 3 things need you. Groceries are being bought now; gpu-box disk is at 91 %; the code fixer is ready to push a fix for nexus.",
      { at: now - 50 * MIN, notes: [{ id: "daily/2026-10-11", path: "daily/2026-10-11.md", title: "2026-10-11" }] },
    );
    this.facts = [
      { id: "fct_1", subject: { id: "people/Rui Costa", path: "people/Rui Costa.md", title: "Rui Costa" }, predicate: "works_at", object: "Sonae", confidence: 0.72, status: FactStatus.PROPOSED, source: { id: "inbox/call notes Rui", path: "inbox/call notes Rui.md", title: "call notes Rui" } },
      { id: "fct_2", subject: { id: "people/Ana Pina", path: "people/Ana Pina.md", title: "Ana Pina" }, predicate: "prefers", object: "lactose-free milk", confidence: 0.91, status: FactStatus.PROPOSED },
      { id: "fct_3", subject: { id: "machines/gpu-box", path: "machines/gpu-box.md", title: "gpu-box" }, predicate: "has_gpu", object: "RTX 4090", confidence: 0.97, status: FactStatus.CONFIRMED },
    ].map((f) => create(FactSchema, f));
    this.notice("", NoticeKind.FACT_REVIEW, "Does Rui Costa work at Sonae?", "Librarian extracted this from your call notes (confidence 72 %).", {
      at: now - 2 * HOUR,
      data: { factId: "fct_1", subject: "Rui Costa", predicate: "works_at", object: "Sonae", confidence: 0.72 },
      notes: [{ id: "people/Rui Costa", path: "people/Rui Costa.md", title: "Rui Costa" }],
    });
    this.notice("", NoticeKind.SUGGESTION, "Let Code fixer push branches in nexus without asking?", "You approved the last 5 pushes from Code fixer to github.com/danielpina1/nexus. Lower code_push to notify for this repo?", {
      at: now - 5 * HOUR,
      data: { agentId: "code-fixer", actionClass: "code_push", from: "approve", to: "notify" },
    });
    const invoices = [...this.tasks.values()].filter((t) => t.agentId === "assistant" && t.state === TaskState.SUCCEEDED).pop();
    if (invoices) {
      this.notice(invoices.id, NoticeKind.RESULT, `Ready: ${invoices.title}`, invoices.summary, { at: tsMsOf(invoices) + 1000 });
    }

    // 1) Groceries workflow: parent + finished "usual list" child + running cart fill.
    const t0 = now - 9 * MIN;
    const wf = this.newTask({ title: "Groceries — the usual", prompt: "buy the usual", workflowId: "groceries", source: "siri", tier: Tier.MEDIUM }, t0);
    this.setTask(wf.id, { state: TaskState.RUNNING }, t0 + 1000);
    const usual = this.newTask({ title: "Build the usual grocery list", prompt: "Build the usual grocery list from the last 4 orders in episodes/shopping/.", agentId: "assistant", parentId: wf.id, tier: Tier.MEDIUM, source: "workflow" }, t0 + 2000);
    this.setTask(usual.id, { state: TaskState.RUNNING, nodeId: "vps" }, t0 + 4000);
    const c0 = this.tool(usual.id, "memory.search", { query: "episodes/shopping last 4 orders" }, ActionClass.READ, t0 + 6000);
    this.toolResult(usual.id, c0, { notes: ["2026-10-04 Groceries", "Shopping preferences"] }, 210, t0 + 7000);
    this.out(usual.id, `Usual list (${GROCERY_ITEMS.length} items):\n` + GROCERY_ITEMS.map(([n, q]) => `- ${q}× ${n}`).join("\n"), t0 + 50_000, true);
    this.cost(usual.id, this.costFor("assistant", 4, 8200, 900), t0 + 52_000);
    this.setTask(usual.id, { state: TaskState.SUCCEEDED, summary: `${GROCERY_ITEMS.length} items from the last 4 orders` }, t0 + 55_000);

    const g0 = now - 7.5 * MIN;
    const g = this.newTask({ title: "Fill Continente cart", prompt: "List: the usual (22 items). Guidelines: budget €160, lactose-free milk.", agentId: "grocery-continente", parentId: wf.id, tier: Tier.MEDIUM, source: "workflow" }, g0);
    this.setTask(g.id, { state: TaskState.ASSIGNED, nodeId: "mac-studio" }, g0 + 1500);
    this.setTask(g.id, { state: TaskState.RUNNING }, g0 + 3000);
    this.out(g.id, "Opening continente.pt with the saved session…\n", g0 + 5000);
    const lc = this.tool(g.id, "browser.open", { url: "https://www.continente.pt" }, ActionClass.READ, g0 + 6000);
    this.toolResult(g.id, lc, { title: "Continente Online", loggedIn: true }, 2400, g0 + 9000);
    this.out(g.id, "Logged in as Daniel. Delivery slot Sat 9–11 available ✓\n\n", g0 + 11_000);
    let tt = g0 + 20_000;
    for (let i = 0; i < 9; i++) {
      this.groceryItem(g.id, i, tt);
      tt += 40_000;
    }
    this.grocery = { taskId: g.id, parentId: wf.id, item: 9, phase: 0, total: GROCERY_ITEMS.slice(0, 9).reduce((s, [, q, p]) => s + q * p, 0), done: false };

    // 2) Code fixer waiting for approval to push.
    const f0 = now - 26 * MIN;
    const fx = this.newTask({ title: "Fix failing TestSubscribeReplay", prompt: "TestSubscribeReplay fails on CI in backend/internal/events — fix it.", agentId: "code-fixer", tier: Tier.HEAVY, source: "ios-share" }, f0);
    this.setTask(fx.id, { state: TaskState.RUNNING, nodeId: "mac-studio" }, f0 + 3000);
    this.codeFixerSteps(fx.id, f0 + 5000, false);
    this.cost(fx.id, this.costFor("code-fixer", 81, 241_000, 18_400), now - 4 * MIN);
    const ap = this.approval(
      fx.id,
      {
        cls: ActionClass.CODE_PUSH,
        level: TrustLevel.APPROVE,
        title: "Push fix/subscribe-replay and open a PR",
        summary: "3 files changed, +42 −13. All 214 tests pass (5 runs). Opens a PR against main in danielpina1/nexus.",
        payload: { repo: "github.com/danielpina1/nexus", branch: "fix/subscribe-replay", base: "main", files: ["backend/internal/events/stream.go", "backend/internal/events/stream_test.go", "backend/internal/events/replay.go"], additions: 42, deletions: 13 },
        previews: [{ id: "att_diff", name: "fix-subscribe-replay.diff", mimeType: "text/x-diff", data: enc.encode(diffText), sizeBytes: BigInt(diffText.length) }],
      },
      now - 4 * MIN,
    );
    this.setTask(fx.id, { state: TaskState.WAITING_APPROVAL }, now - 4 * MIN + 500);
    this.onDecision.set(ap.id, (ok) => void this.finishCodeFix(fx.id, ok));

    // 3) Ops cleanup waiting for a biometric approval.
    const o0 = now - 41 * MIN;
    const ops = this.newTask({ title: "Free disk space on gpu-box", prompt: "gpu-box disk is at 91 % — find what's using space and clean up.", agentId: "ops", tier: Tier.MEDIUM, source: "trigger" }, o0);
    this.setTask(ops.id, { state: TaskState.RUNNING, nodeId: "gpu-box" }, o0 + 2000);
    const oc = this.tool(ops.id, "node.exec", { node: "gpu-box", cmd: "du -sh /home/daniel/models/* /var/lib/docker | sort -h" }, ActionClass.READ, o0 + 5000);
    this.toolResult(ops.id, oc, { stdout: "540M nomic-embed-v2.gguf\n1.2G router-classifier-v3\n10G qwen3-14b-q5.gguf\n21G llama-4-scout-q4.gguf\n17G /var/lib/docker" }, 3100, o0 + 9000);
    this.out(ops.id, "Disk is 91 % full. Biggest items: llama-4-scout (21 GB, unused for 14 days) and 17 GB of dangling Docker layers.\n\nProposed cleanup frees **38 GB** (→ 64 %).", o0 + 20_000);
    this.cost(ops.id, this.costFor("ops", 3, 6100, 700), o0 + 21_000);
    const oa = this.approval(
      ops.id,
      {
        cls: ActionClass.DESTRUCTIVE,
        level: TrustLevel.BIOMETRIC,
        title: "Delete 38 GB on gpu-box",
        summary: "Remove llama-4-scout-q4.gguf (21 GB, unused 14 days) and prune dangling Docker images (17 GB).",
        payload: { node: "gpu-box", commands: ["rm /home/daniel/models/llama-4-scout-q4.gguf", "docker image prune -af --filter until=336h"], frees_gb: 38 },
        expiresIn: 3 * HOUR,
      },
      o0 + 22_000,
    );
    this.setTask(ops.id, { state: TaskState.WAITING_APPROVAL }, o0 + 23_000);
    this.onDecision.set(oa.id, (ok) => void this.finishOps(ops.id, ok));
    this.notice(ops.id, NoticeKind.ALERT, "gpu-box disk at 91 %", "Watchdog: disk usage crossed 90 %. Ops proposed a cleanup that needs your Face ID.", { at: o0 - 1000 });

    // 4) Assistant asking a question.
    const q0 = now - 14 * MIN;
    const dent = this.newTask({ title: "Book a dentist appointment", prompt: "Book a cleaning at the dentist next week, afternoons are better.", agentId: "assistant", tier: Tier.MEDIUM, source: "ios-chat" }, q0);
    this.setTask(dent.id, { state: TaskState.RUNNING, nodeId: "vps" }, q0 + 2000);
    const dc = this.tool(dent.id, "calendar.free_slots", { week: "2026-W42", after: "14:00" }, ActionClass.READ, q0 + 4000);
    this.toolResult(dent.id, dc, { slots: ["Tue 10:00", "Thu 15:30"] }, 640, q0 + 6000);
    this.out(dent.id, "Clínica Dr. Silva has two openings next week that fit your calendar.\n", q0 + 8000);
    const qid = this.question(dent.id, "Which slot should I book with Dr. Silva?", ["Tue 14 Oct, 10:00", "Thu 16 Oct, 15:30", "Neither — look at the week after"], true, q0 + 9000);
    this.setTask(dent.id, { state: TaskState.WAITING_INPUT }, q0 + 9500);
    this.cost(dent.id, this.costFor("assistant", 2, 3100, 300), q0 + 9600);
    this.onAnswer.set(qid, (ans) => void this.finishDentist(dent.id, ans));

    // 5) Librarian processing captures (streams continuously).
    this.startLibrarian(now - 2 * MIN);
  }

  private groceryItem(id: string, i: number, at?: number) {
    const [name, qty, price] = GROCERY_ITEMS[i];
    const c = this.tool(id, "browser.search", { query: name, store: "continente.pt" }, ActionClass.READ, at);
    this.toolResult(id, c, { best: name, price_eur: price / 100, in_stock: true }, 1800 + ((i * 337) % 1500), at ? at + 2000 : undefined);
    this.out(id, `✓ ${qty}× ${name} — €${((qty * price) / 100).toFixed(2)}\n`, at ? at + 3000 : undefined);
    if (i % 3 === 2) this.cost(id, this.costFor("grocery-continente", 3, 7400, 380), at ? at + 3500 : undefined);
  }

  private codeFixerSteps(id: string, start: number, live: boolean) {
    const step = (ms: number) => (live ? undefined : start + ms);
    this.out(id, "Creating worktree ~/.nexus/worktrees/fix-subscribe-replay…\n", step(0));
    const c1 = this.tool(id, "bash", { cmd: "go test ./internal/events/... -run TestSubscribeReplay -count=20" }, ActionClass.COMPUTE, step(5000));
    this.toolResult(id, c1, { exit: 1, stdout: "--- FAIL: TestSubscribeReplay (0.02s)\n    stream_test.go:88: got 9 events, want 10\nFAIL (7/20 runs)" }, 18_400, step(25_000));
    this.out(id, "Reproduced: 7/20 runs fail. The subscriber can miss the event published between the replay query and attaching the live listener.\n", step(40_000));
    const c2 = this.tool(id, "edit", { file: "backend/internal/events/stream.go", change: "attach listener before replay; dedupe by seq" }, ActionClass.WRITE_INTERNAL, step(90_000));
    this.toolResult(id, c2, { ok: true }, 900, step(92_000));
    const c3 = this.tool(id, "bash", { cmd: "go test ./... -count=5" }, ActionClass.COMPUTE, step(120_000));
    this.toolResult(id, c3, { exit: 0, stdout: "ok  \tgithub.com/danielpina1/nexus/backend/... 214 tests, 5 runs" }, 61_000, step(181_000));
    this.emit(id, { case: "artifact", value: { attachment: { id: "att_diff", name: "fix-subscribe-replay.diff", mimeType: "text/x-diff", data: enc.encode(diffText), sizeBytes: BigInt(diffText.length) } } }, { at: step(200_000) });
    this.out(id, "Fixed: listener is attached before the replay query and duplicates are dropped by seq. All tests pass (5 runs). Ready to push.\n", step(210_000));
  }

  private async finishCodeFix(id: string, ok: boolean) {
    this.setTask(id, { state: TaskState.RUNNING });
    await sleep(1200);
    if (!ok) {
      this.out(id, "Push denied — keeping the fix on the local branch fix/subscribe-replay.\n", undefined, true);
      this.setTask(id, { state: TaskState.SUCCEEDED, summary: "Fix kept locally on fix/subscribe-replay (push denied)." });
      return;
    }
    const c = this.tool(id, "git.push", { branch: "fix/subscribe-replay", remote: "origin" }, ActionClass.CODE_PUSH);
    await sleep(1800);
    this.toolResult(id, c, { pushed: true }, 1700);
    const p = this.tool(id, "github.create_pr", { title: "events: attach listener before replay", base: "main" }, ActionClass.WRITE_EXTERNAL);
    await sleep(1500);
    this.toolResult(id, p, { number: 142, url: "https://github.com/danielpina1/nexus/pull/142" }, 1400);
    this.out(id, "Opened PR #142 — events: attach listener before replay. CI is running.\n", undefined, true);
    this.cost(id, this.costFor("code-fixer", 6, 12_000, 900));
    this.setTask(id, { state: TaskState.SUCCEEDED, summary: "PR #142 opened: attach listener before replay." });
    this.notice(id, NoticeKind.RESULT, "PR #142 opened", "Code fixer pushed fix/subscribe-replay and opened a PR. CI is running.");
  }

  private async finishOps(id: string, ok: boolean) {
    if (!ok) {
      this.out(id, "Cleanup denied. Nothing was deleted.\n", undefined, true);
      this.setTask(id, { state: TaskState.CANCELLED, summary: "Cleanup denied by owner." });
      return;
    }
    this.setTask(id, { state: TaskState.RUNNING });
    await sleep(2500);
    this.out(id, "Deleted 38 GB. gpu-box disk now at 64 %.\n", undefined, true);
    this.setTask(id, { state: TaskState.SUCCEEDED, summary: "Freed 38 GB on gpu-box (91 % → 64 %)." });
  }

  private async finishDentist(id: string, ans: string) {
    this.setTask(id, { state: TaskState.RUNNING });
    await sleep(1500);
    if (/neither/i.test(ans)) {
      this.out(id, "OK — I'll look at the week of 20 October and come back to you.\n", undefined, true);
      this.setTask(id, { state: TaskState.SUCCEEDED, summary: "Will look at the following week." });
      return;
    }
    const c = this.tool(id, "browser.fill_form", { site: "clinicasilva.pt", slot: ans }, ActionClass.WRITE_EXTERNAL);
    await sleep(2500);
    this.toolResult(id, c, { confirmed: true, ref: "CS-20931" }, 2300);
    const c2 = this.tool(id, "calendar.create_event", { title: "Dentist — cleaning", when: ans }, ActionClass.WRITE_INTERNAL);
    await sleep(800);
    this.toolResult(id, c2, { ok: true }, 300);
    this.out(id, `Booked **${ans}** with Dr. Silva (ref CS-20931) and added it to your calendar.\n`, undefined, true);
    this.cost(id, this.costFor("assistant", 3, 4200, 350));
    this.setTask(id, { state: TaskState.SUCCEEDED, summary: `Dentist booked: ${ans}` });
    this.notice(id, NoticeKind.RESULT, "Dentist booked", `${ans} with Dr. Silva — added to your calendar.`);
  }

  private startLibrarian(at = Date.now()) {
    const t = this.newTask(
      { title: `Process ${CAPTURES.length} new captures`, prompt: "Process new notes in inbox/ and episodes/.", agentId: "librarian", tier: Tier.SIMPLE, source: "trigger" },
      at,
    );
    this.setTask(t.id, { state: TaskState.RUNNING, nodeId: "gpu-box" }, at + 1000);
    this.out(t.id, `Found ${CAPTURES.length} new notes. Using fast-local on gpu-box.\n\n`, at + 2000);
    this.librarian = { taskId: t.id, i: 0, phase: 0, batch: this.librarian.batch + 1, cooldown: 0 };
  }

  // ---- live simulation -------------------------------------------------------------------------

  private onTick() {
    this.tick++;
    this.tickGrocery();
    this.tickLibrarian();
    if (this.tick % 3 === 0) this.tickNodes();
  }

  private tickGrocery() {
    const g = this.grocery;
    if (!g.taskId || g.done || !this.alive(g.taskId)) return;
    if (this.tasks.get(g.taskId)?.state !== TaskState.RUNNING) return;
    if (g.item < GROCERY_ITEMS.length) {
      const [name, qty, price] = GROCERY_ITEMS[g.item];
      if (g.phase === 0) {
        (g as { call?: string }).call = this.tool(g.taskId, "browser.search", { query: name, store: "continente.pt" }, ActionClass.READ);
        g.phase = 1;
      } else {
        const callId = (g as { call?: string }).call ?? "";
        const sub = g.item === 13;
        this.toolResult(g.taskId, callId, sub ? { best: "Tomate rama (1kg)", price_eur: 2.49, in_stock: true, substituted: true } : { best: name, price_eur: price / 100, in_stock: true }, 1500 + (g.item % 4) * 400);
        this.out(g.taskId, sub ? `↺ ${qty}× Tomate rama (1kg) — €2.49 (chucha out of stock; same price)\n` : `✓ ${qty}× ${name} — €${((qty * price) / 100).toFixed(2)}\n`);
        g.total += qty * price;
        if (g.item % 3 === 2) this.cost(g.taskId, this.costFor("grocery-continente", 3, 7400, 380));
        g.item++;
        g.phase = 0;
      }
      return;
    }
    // Cart complete → screenshot + biometric approval for checkout.
    g.done = true;
    const total = g.total + 399; // delivery fee
    const id = g.taskId;
    this.out(id, `\nCart ready: ${GROCERY_ITEMS.length} items · €${(total / 100).toFixed(2)} incl. €3.99 delivery · Sat 9–11. 1 substitution.\n`);
    const svg = cartSvg(GROCERY_ITEMS.length, total);
    this.emit(id, { case: "artifact", value: { attachment: { id: "att_cart", name: "cart.svg", mimeType: "image/svg+xml", data: enc.encode(svg), sizeBytes: BigInt(svg.length) } } });
    this.cost(id, this.costFor("grocery-continente", 2, 5200, 300));
    const a = this.approval(id, {
      cls: ActionClass.SPEND,
      level: TrustLevel.BIOMETRIC,
      title: `Checkout €${(total / 100).toFixed(2)} at Continente`,
      summary: `${GROCERY_ITEMS.length} items, 1 substitution (tomato). Delivery Sat 9–11 to home. Saved card ending 4417.`,
      payload: { store: "continente.pt", items: GROCERY_ITEMS.length, total_eur: total / 100, delivery: "Sat 9–11", payment: "card •••• 4417" },
      previews: [{ id: "att_cart", name: "cart.svg", mimeType: "image/svg+xml", data: enc.encode(svg), sizeBytes: BigInt(svg.length) }],
      expiresIn: 30 * MIN,
    });
    this.setTask(id, { state: TaskState.WAITING_APPROVAL });
    this.setTask(g.parentId, { state: TaskState.WAITING_APPROVAL });
    this.onDecision.set(a.id, (ok) => void this.finishGrocery(id, g.parentId, ok, total));
    // Demo: the owner approves on the iPhone with Face ID after a while.
    setTimeout(() => {
      const cur = this.approvals.get(a.id);
      if (cur?.state === ApprovalState.PENDING) this.decideInternal(a.id, true, "device:dev_iphone");
    }, 75_000);
  }

  private async finishGrocery(id: string, parentId: string, ok: boolean, total: number) {
    if (!ok) {
      this.out(id, "Checkout denied. The cart stays saved on continente.pt.\n", undefined, true);
      this.setTask(id, { state: TaskState.CANCELLED, summary: "Checkout denied; cart kept." });
      this.setTask(parentId, { state: TaskState.CANCELLED, summary: "Checkout denied." });
      return;
    }
    this.setTask(id, { state: TaskState.RUNNING });
    this.setTask(parentId, { state: TaskState.RUNNING });
    this.out(id, "Approved with Face ID on iPhone. Checking out…\n");
    const c = this.tool(id, "browser.checkout", { payment: "saved card •••• 4417", slot: "Sat 9–11" }, ActionClass.SPEND);
    await sleep(4000);
    this.toolResult(id, c, { order: "CNT-48213", total_eur: total / 100 }, 3900);
    this.out(id, `Order CNT-48213 placed — €${(total / 100).toFixed(2)}, delivery Saturday 9–11.\n`, undefined, true);
    this.cost(id, this.costFor("grocery-continente", 2, 3000, 200));
    this.setTask(id, { state: TaskState.SUCCEEDED, summary: `Order CNT-48213 · €${(total / 100).toFixed(2)} · Sat 9–11` });
    this.setTask(parentId, { state: TaskState.SUCCEEDED, summary: `Groceries ordered (€${(total / 100).toFixed(2)})` });
    this.notice(id, NoticeKind.RESULT, `Groceries ordered — €${(total / 100).toFixed(2)}`, "Order CNT-48213, delivery Saturday 9–11. Episode note written.", {
      notes: [{ id: "episodes/shopping/2026-10-04 Groceries", path: "episodes/shopping/2026-10-04 Groceries.md", title: "2026-10-04 Groceries" }],
    });
  }

  private tickLibrarian() {
    const l = this.librarian;
    if (!l.taskId) return;
    if (!this.alive(l.taskId)) {
      l.cooldown++;
      if (l.cooldown > 12) this.startLibrarian();
      return;
    }
    if (this.tasks.get(l.taskId)?.state !== TaskState.RUNNING) return;
    if (l.i >= CAPTURES.length) {
      this.out(l.taskId, `\nDone. Filed ${CAPTURES.length} notes, created 2 entity notes, proposed 4 facts.`, undefined, true);
      this.setTask(l.taskId, { state: TaskState.SUCCEEDED, summary: `Filed ${CAPTURES.length} notes, proposed 4 facts.` });
      return;
    }
    const [path, what, link] = CAPTURES[l.i];
    switch (l.phase) {
      case 0:
        this.out(l.taskId, `→ ${path}\n  reading ${what}…`);
        break;
      case 1: {
        const c = this.tool(l.taskId, "vault.link", { note: path, link }, ActionClass.WRITE_INTERNAL);
        this.toolResult(l.taskId, c, { linked: link }, 120 + (l.i % 5) * 40);
        this.out(l.taskId, ` summarized, linked ${link}\n`);
        break;
      }
      case 2:
        this.cost(l.taskId, this.costFor("librarian", 0, 2400 + l.i * 90, 310));
        l.i++;
        l.phase = -1;
        break;
    }
    l.phase++;
  }

  private tickNodes() {
    const active = (nodeId: string) => [...this.tasks.values()].filter((t) => t.nodeId === nodeId && !TERMINAL.has(t.state)).length;
    for (const n of this.nodes) {
      const s = n.status!;
      const base = n.id === "gpu-box" ? 70 : n.id === "mac-studio" ? 35 : 8;
      s.cpuPercent = clamp(base + (Math.sin(this.tick / 5 + n.id.length) * 12 + (Math.random() - 0.5) * 10), 1, 99);
      s.memoryPercent = clamp(s.memoryPercent + (Math.random() - 0.5) * 2, 20, 95);
      s.activeRuns = active(n.id);
      if (s.online) s.lastSeen = ts(Date.now());
      this.emit("", { case: "nodeStatus", value: clone(NodeStatusSchema, s) }, { actor: `node:${n.id}` });
    }
  }

  // ---- service operations ----------------------------------------------------------------------

  decideInternal(approvalId: string, approve: boolean, actor: string) {
    const a = this.approvals.get(approvalId);
    if (!a) throw new ConnectError("approval not found", Code.NotFound);
    if (a.state !== ApprovalState.PENDING) throw new ConnectError("approval already decided", Code.FailedPrecondition);
    a.state = approve ? ApprovalState.APPROVED : ApprovalState.DENIED;
    this.emit(a.taskId, { case: "approval", value: clone(ApprovalSchema, a) }, { actor });
    const cb = this.onDecision.get(approvalId);
    this.onDecision.delete(approvalId);
    cb?.(approve);
    return a;
  }

  decide(approvalId: string, approve: boolean, signature: Uint8Array, deviceId: string): Approval {
    const a = this.approvals.get(approvalId);
    if (a && approve && a.level === TrustLevel.BIOMETRIC && signature.length === 0) {
      throw new ConnectError("Biometric approvals must be signed with Face ID on a paired iPhone.", Code.PermissionDenied);
    }
    return this.decideInternal(approvalId, approve, `device:${deviceId || "web"}`);
  }

  sendMessage(taskId: string, text: string, questionId: string, choice: string) {
    const t = this.tasks.get(taskId);
    if (!t) throw new ConnectError("task not found", Code.NotFound);
    const answer = choice || text;
    this.emit(taskId, { case: "userMessage", value: { text: answer } }, { actor: "device:web" });
    const cb = questionId ? this.onAnswer.get(questionId) : undefined;
    if (cb) {
      this.onAnswer.delete(questionId);
      cb(answer);
      return;
    }
    const mcb = this.onMessage.get(taskId);
    if (mcb) {
      mcb(answer);
      return;
    }
    if (!TERMINAL.has(t.state)) {
      setTimeout(() => this.out(taskId, `\n(Noted: “${answer}” — I'll take that into account.)\n`), 900);
    } else {
      // Follow-up on a finished task starts a child task.
      this.runGeneric(answer, t.agentId || "assistant", { parentId: t.id });
    }
  }

  cancel(id: string) {
    const t = this.tasks.get(id);
    if (!t) throw new ConnectError("task not found", Code.NotFound);
    if (TERMINAL.has(t.state)) return;
    for (const a of this.approvals.values()) {
      if (a.taskId === id && a.state === ApprovalState.PENDING) {
        a.state = ApprovalState.EXPIRED;
        this.emit(id, { case: "approval", value: clone(ApprovalSchema, a) });
      }
    }
    this.out(id, "\nCancelled by owner.\n", undefined, true);
    this.setTask(id, { state: TaskState.CANCELLED, summary: "Cancelled by owner." }, Date.now(), "device:web");
    for (const c of this.tasks.values()) if (c.parentId === id) this.cancel(c.id);
  }

  stopAll(): number {
    let n = 0;
    for (const t of [...this.tasks.values()]) {
      if (!TERMINAL.has(t.state) && !t.parentId) {
        this.cancel(t.id);
        n++;
      }
    }
    for (const t of [...this.tasks.values()]) {
      if (!TERMINAL.has(t.state)) {
        this.cancel(t.id);
        n++;
      }
    }
    this.notice("", NoticeKind.ALERT, "Kill switch engaged", `${n} task${n === 1 ? "" : "s"} stopped from the web dashboard. Triggers paused.`);
    return n;
  }

  escalate(id: string, hint: string): Task {
    const t = this.tasks.get(id);
    if (!t) throw new ConnectError("task not found", Code.NotFound);
    const next = Math.min(Tier.HEAVY, (t.tier || Tier.SIMPLE) + 1) as Tier;
    if (!TERMINAL.has(t.state)) this.setTask(id, { state: TaskState.ESCALATING });
    const agent = next === Tier.HEAVY && t.agentId === "assistant" ? "assistant" : t.agentId || "assistant";
    const child = this.runGeneric(t.prompt + (hint ? `\n\nHint: ${hint}` : ""), agent, { parentId: id, tier: next, title: `${t.title} (try harder)` });
    if (!TERMINAL.has(t.state)) setTimeout(() => this.setTask(id, { state: TaskState.SUCCEEDED, summary: "Escalated to the next tier." }), 600);
    return child;
  }

  route(input: string): Route & { kindLabel?: string } {
    const s = input.toLowerCase();
    const mk = (kind: CommandKind, agentId: string, confidence: number, reason: string, alternatives: string[] = []) =>
      ({ kind, agentId, workflowId: "", tier: this.agentFor(agentId).tier, confidence, reason, alternatives }) as unknown as Route;
    if (/^(remember|note|capture|save)\b/.test(s)) return mk(CommandKind.CAPTURE, "librarian", 0.94, "Starts with a capture verb");
    if (/^(what|who|when|where|which|how much|find|search)\b/.test(s) && !/\b(fix|buy|book|restart)\b/.test(s))
      return mk(CommandKind.SEARCH, "", 0.86, "Question about your notes", ["assistant"]);
    if (/(grocer|compras|supermerc|continente|buy the usual|shopping)/.test(s)) return mk(CommandKind.TASK, "grocery-continente", 0.95, "Matches grocery trigger phrases");
    if (/(fix|bug|test|refactor|pr\b|pull request|compile|lint|repo)/.test(s)) return mk(CommandKind.TASK, "code-fixer", 0.88, "Code change in a repository", ["assistant"]);
    if (/(server|disk|restart|backup|machine|cpu|vps|gpu-box|mac-studio|logs?)\b/.test(s)) return mk(CommandKind.TASK, "ops", 0.84, "Machine operations", ["assistant"]);
    if (s.split(/\s+/).length <= 2) return mk(CommandKind.TASK, "", 0.41, "Too short to route confidently", ["assistant", "ops", "code-fixer"]);
    return mk(CommandKind.TASK, "assistant", 0.79, "General request; no specialist matched", ["ops"]);
  }

  capture(text: string, source: string): NoteRef {
    const d = new Date();
    const stamp = `${d.toISOString().slice(0, 10)} ${d.toTimeString().slice(0, 5).replace(":", "")}`;
    const title = text.replace(/^(remember|note|capture|save)( that)?[:\s]*/i, "").slice(0, 48) || "capture";
    const path = `inbox/${stamp} ${title.replace(/[/\\:]/g, "-")}.md`;
    const n = upsertNote(path, `---\ntype: capture\ntitle: ${title}\nsource: ${source}\ntags: [inbox]\n---\n\n${text}\n`);
    return create(NoteRefSchema, { id: n.id, path: n.path, title: n.title });
  }

  /** Runs a scripted task for an agent with streamed output. */
  runGeneric(prompt: string, agentId: string, opts: { parentId?: string; tier?: Tier; title?: string; source?: string } = {}): Task {
    const rt = this.agentFor(agentId);
    const title = opts.title ?? titleFor(prompt);
    const t = this.newTask({ title, prompt, agentId, tier: opts.tier ?? rt.tier, parentId: opts.parentId ?? "", source: opts.source ?? "web" });
    void this.script(t.id, agentId, prompt);
    return t;
  }

  private async script(id: string, agentId: string, prompt: string) {
    const rt = this.agentFor(agentId);
    const ok = () => this.alive(id);
    await sleep(500);
    if (!ok()) return;
    this.setTask(id, { state: TaskState.ROUTING });
    await sleep(700);
    if (!ok()) return;
    this.setTask(id, { state: TaskState.RUNNING, nodeId: rt.node });
    await sleep(600);

    if (agentId === "code-fixer") {
      for (const [ms, fn] of [
        [800, () => this.out(id, "Creating a fresh worktree on mac-studio…\n")],
        [1500, () => { const c = this.tool(id, "bash", { cmd: "go test ./... 2>&1 | tail -20" }, ActionClass.COMPUTE); setTimeout(() => this.toolResult(id, c, { exit: 1, stdout: "FAIL internal/policy 1 test" }, 4100), 1400); }],
        [2600, () => this.out(id, "Reproduced the failure. Looking at the policy evaluator…\n")],
        [2200, () => { const c = this.tool(id, "edit", { file: "backend/internal/policy/eval.go" }, ActionClass.WRITE_INTERNAL); setTimeout(() => this.toolResult(id, c, { ok: true }, 600), 700); }],
        [1800, () => { const c = this.tool(id, "bash", { cmd: "go test ./... -count=3" }, ActionClass.COMPUTE); setTimeout(() => this.toolResult(id, c, { exit: 0, stdout: "ok — 214 tests" }, 9000), 2200); }],
        [2600, () => this.out(id, "All tests pass. Proposed change: 1 file, +8 −3.\n")],
      ] as Array<[number, () => void]>) {
        await sleep(ms);
        if (!ok()) return;
        fn();
      }
      this.cost(id, this.costFor("code-fixer", 64, 120_000, 9000));
      const a = this.approval(id, {
        cls: ActionClass.CODE_PUSH,
        level: TrustLevel.APPROVE,
        title: "Push branch and open a PR",
        summary: `“${titleFor(prompt)}” — 1 file changed, +8 −3, tests green.`,
        payload: { repo: "github.com/danielpina1/nexus", branch: "fix/" + slug(prompt), additions: 8, deletions: 3 },
      });
      this.setTask(id, { state: TaskState.WAITING_APPROVAL });
      this.onDecision.set(a.id, (yes) => void this.finishCodeFix(id, yes));
      return;
    }

    if (agentId === "grocery-continente") {
      this.out(id, "Got it. Before I start a cart: ");
      await sleep(900);
      const q = this.question(id, "Add these to the usual list, or buy only these items?", ["Add to the usual", "Only these items"], false);
      this.setTask(id, { state: TaskState.WAITING_INPUT });
      this.onAnswer.set(q, async (ans) => {
        this.setTask(id, { state: TaskState.RUNNING });
        await this.stream(id, `OK — ${ans.toLowerCase()}. Searching Continente for each item… found everything in stock. Cart saved; I'll ask before checking out.`);
        this.cost(id, this.costFor(agentId, 9, 21_000, 1200));
        this.setTask(id, { state: TaskState.SUCCEEDED, summary: "Cart prepared on continente.pt" });
      });
      return;
    }

    if (agentId === "ops") {
      const c = this.tool(id, "node.exec", { node: "vps", cmd: "uptime; df -h /; systemctl --failed" }, ActionClass.READ);
      await sleep(1600);
      if (!ok()) return;
      this.toolResult(id, c, { stdout: "load average: 0.21 0.18 0.12\n/dev/sda1 62% used\n0 loaded units listed." }, 1500);
      await this.stream(id, "**vps** looks healthy: load 0.21, disk 62 %, no failed units. **gpu-box** is the one to watch — disk at 91 % with a cleanup waiting for your approval. **mac-studio** is busy with 2 runs but fine.");
      this.cost(id, this.costFor(agentId, 2, 4100, 380));
      if (!ok()) return;
      this.setTask(id, { state: TaskState.SUCCEEDED, summary: "All machines healthy except gpu-box disk (91 %)." });
      return;
    }

    // "stress [n]": streams n output chunks as fast as possible (perf testing; see README).
    const stress = /^stress\b\s*(\d+)?/i.exec(prompt.trim());
    if (stress) {
      const total = Math.min(50_000, Number(stress[1] ?? 5000));
      const t0 = performance.now();
      for (let i = 0; i < total; i++) {
        if (!ok()) return;
        const chunk =
          i % 40 === 0
            ? `\n\n### Section ${i / 40 + 1}\n\n`
            : i % 7 === 0
              ? `- item **${i}** with \`code\` and a [[Ana]] link\n`
              : `token${i} `;
        this.out(id, chunk, undefined, i === total - 1);
        if (i % 20 === 19) await sleep(4);
      }
      this.setTask(id, { state: TaskState.SUCCEEDED, summary: `Streamed ${total} chunks in ${Math.round(performance.now() - t0)} ms` });
      return;
    }

    // assistant / librarian / default
    const c = this.tool(id, "memory.search", { query: prompt.slice(0, 80) }, ActionClass.READ);
    await sleep(1100);
    if (!ok()) return;
    const hits = searchNotes(prompt, 3);
    this.toolResult(id, c, { notes: hits.map((h) => h.title) }, 240);
    const cites = hits.map((h) => `[[${h.title}]]`).join(", ");
    await this.stream(
      id,
      `Here's what I'd do for “${prompt.trim()}”:\n\n1. **Context** — I checked your vault${cites ? ` (${cites})` : ""} for anything relevant.\n2. **Plan** — break it into small steps and hand anything specialised to the right agent.\n3. **Next** — I can schedule a follow-up or start right away.\n\nWant me to go ahead?`,
    );
    if (!ok()) return;
    this.cost(id, this.costFor(agentId, agentId === "librarian" ? 0 : 4 + Math.round(Math.random() * 6), 5200, 640));
    this.setTask(id, { state: TaskState.SUCCEEDED, summary: `Plan for: ${titleFor(prompt)}` });
  }

  private async stream(id: string, text: string) {
    const parts = text.match(/\S+\s*/g) ?? [text];
    let buf = "";
    for (let i = 0; i < parts.length; i++) {
      buf += parts[i];
      if (buf.length > 14 || i === parts.length - 1) {
        if (!this.alive(id)) return;
        this.out(id, buf, undefined, i === parts.length - 1);
        buf = "";
        await sleep(90 + Math.random() * 120);
      }
    }
  }

  // ---- agents ----------------------------------------------------------------------------------

  listAgents(): Agent[] {
    return [...this.agentMarkdown.entries()].map(([id, md]) => parseAgentMarkdown(md, `agents/${id}.md`)).sort((a, b) => a.name.localeCompare(b.name));
  }

  upsertAgent(markdown: string): Agent {
    const agent = parseAgentMarkdown(markdown);
    if (agent.errors.length) {
      throw new ConnectError(agent.errors.join("\n"), Code.InvalidArgument);
    }
    const prev = this.agentMarkdown.get(agent.id);
    let md = markdown;
    if (prev) {
      const prevVersion = parseAgentMarkdown(prev).version;
      if (agent.version <= prevVersion) {
        md = markdown.replace(/^version:\s*\d+\s*$/m, `version: ${prevVersion + 1}`);
        if (md === markdown) md = markdown.replace(/\n---/, `\nversion: ${prevVersion + 1}\n---`);
      }
    }
    this.agentMarkdown.set(agent.id, md);
    upsertNote(`agents/${agent.id}.md`, md);
    return parseAgentMarkdown(md, `agents/${agent.id}.md`);
  }
}

// --- helpers -------------------------------------------------------------------------------------

function clamp(v: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, Math.round(v * 10) / 10));
}

function tsMsOf(t: Task): number {
  return t.updatedAt ? Number(t.updatedAt.seconds) * 1000 : Date.now();
}

function asId(n: { path: string; frontmatter: Record<string, unknown> }): string {
  return String(n.frontmatter.id ?? n.path.split("/").pop()?.replace(/\.md$/, ""));
}

function titleFor(prompt: string): string {
  const s = prompt.trim().split("\n")[0].replace(/\s+/g, " ");
  const t = s.length > 64 ? s.slice(0, 61) + "…" : s;
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function slug(s: string) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 28);
}

export function searchNotes(query: string, limit = 10) {
  const terms = query.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter((t) => t.length > 2);
  if (terms.length === 0) return [];
  const scored = notes
    .map((n) => {
      const title = n.title.toLowerCase();
      const body = n.raw.toLowerCase();
      let score = 0;
      for (const t of terms) {
        if (title.includes(t)) score += 5;
        const m = body.split(t).length - 1;
        score += Math.min(m, 5);
      }
      return { n, score };
    })
    .filter((x) => x.score > 0)
    .sort((a, b) => b.score - a.score)
    .slice(0, limit);
  return scored.map(({ n, score }) => {
    const body = n.body.replace(/\s+/g, " ");
    const lower = body.toLowerCase();
    const idx = Math.max(0, ...terms.map((t) => lower.indexOf(t)).filter((i) => i >= 0).slice(0, 1));
    const start = Math.max(0, idx - 50);
    const snippet = (start > 0 ? "…" : "") + body.slice(start, start + 160).trim() + (start + 160 < body.length ? "…" : "");
    return { id: n.id, path: n.path, title: n.title, snippet, score };
  });
}

export { findNote };
