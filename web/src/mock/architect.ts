import { clone, create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  AgentDraftSchema,
  DraftKind,
  DraftNoteSchema,
  DraftState,
  DraftTestCaseSchema,
  DraftTestResultSchema,
  type AgentDraft,
  type DraftNote,
  type DraftTestCase,
  type DraftTestResult,
} from "../gen/nexus/v1/architect_pb";
import { ActionClass, TrustLevel } from "../gen/nexus/v1/common_pb";
import { NoticeKind } from "../gen/nexus/v1/event_pb";
import type { Approval } from "../gen/nexus/v1/approval_pb";
import type { MockHub } from "./hub";
import { DAY, HOUR, MIN, newId, ts } from "./util";

/** Line-level unified diff (LCS); notes are short, so O(n·m) is fine. */
export function unifiedDiff(a: string, b: string, path: string): string {
  const x = a.split("\n");
  const y = b.split("\n");
  const n = x.length;
  const m = y.length;
  const L = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = x[i] === y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
  const ops: Array<[" " | "-" | "+", string, number, number]> = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) ops.push([" ", x[i], i++, j++]);
    else if (j < m && (i >= n || L[i][j + 1] >= L[i + 1][j])) ops.push(["+", y[j], i, j++]);
    else ops.push(["-", x[i], i++, j]);
  }
  // Group into hunks with 2 lines of context.
  const out = [`--- a/${path}`, `+++ b/${path}`];
  const changed = ops.map((o, k) => (o[0] !== " " ? k : -1)).filter((k) => k >= 0);
  if (!changed.length) return "";
  let k = 0;
  while (k < changed.length) {
    const start = Math.max(0, changed[k] - 2);
    let end = Math.min(ops.length - 1, changed[k] + 2);
    while (k + 1 < changed.length && changed[k + 1] - 2 <= end + 1) {
      k++;
      end = Math.min(ops.length - 1, changed[k] + 2);
    }
    k++;
    const hunk = ops.slice(start, end + 1);
    const a0 = hunk[0][2] + 1;
    const b0 = hunk[0][3] + 1;
    const aLen = hunk.filter((o) => o[0] !== "+").length;
    const bLen = hunk.filter((o) => o[0] !== "-").length;
    out.push(`@@ -${a0},${aLen} +${b0},${bLen} @@`);
    for (const o of hunk) out.push(o[0] + o[1]);
  }
  return out.join("\n") + "\n";
}

const slug = (s: string) =>
  s
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .split("-")
    .filter((w) => w && !["a", "an", "the", "me", "my", "when", "tell", "that", "and", "for", "to", "of", "in", "on", "i", "every", "agent"].includes(w))
    .slice(0, 3)
    .join("-") || "new-agent";

interface Design {
  notes: DraftNote[];
  rationale: string;
  risks: string[];
  cases: DraftTestCase[];
  problems: string[];
  fixes: string[];
}

function agentNote(id: string, name: string, description: string, tools: string[], body: string, extra = ""): string {
  return `---
type: agent
id: ${id}
name: ${name}
description: ${description}
icon: sparkles
runtime: openai
model: smart
tier: medium
node: { tags: [any] }
tools: [${tools.join(", ")}]
limits: { max_runtime: 10m, max_cost_eur: 0.20 }
policy: { spend: biometric, write_external: approve }
${extra}version: 1
---

${body}
`;
}

function design(request: string, kind: DraftKind, feedback: string[]): Design {
  const r = request.toLowerCase();
  const wantsCode = /(github|issue|repo|pull request|\bpr\b|fix (the )?bug|code)/.test(r);
  const coding = /(fix|implement|refactor)/.test(r) && /(repo|code|bug)/.test(r);
  const scheduled = /(every|daily|weekly|each (day|week)|morning|friday|monday|when|alert|watch|track|drop below|below €)/.test(r);
  const price = /(price|below|under|€|drop)/.test(r);
  const team = kind === DraftKind.TEAM || /\b(team|crew|squad)\b/.test(r);
  const dropAnthrex = feedback.some((f) => /anthrex|coding|code|drop|remove/i.test(f));
  let id = price ? `${slug(r.replace(/(tell me|alert me|notify me) when|drop(s)? below.*$|under.*$/g, "")) || "price"}-price-watch` : slug(r);
  if (id.length > 40) id = id.slice(0, 40).replace(/-$/, "");
  const name = id.replace(/-/g, " ").replace(/^./, (c) => c.toUpperCase());
  const notes: DraftNote[] = [];
  const fixes = ["runtime \"auto\" is not installed on any node → set to openai (smart)", "limits missing → max_runtime 10m, max_cost_eur 0.20 (defaults)", "policy raised to the defaults: spend: biometric, write_external: approve"];
  const problems: string[] = [];
  let tools = ["web.search", "web.fetch", "memory.search", "notify"];
  if (price) tools = ["web.fetch", "web.extract", "memory.search", "notify"];
  if (wantsCode && !coding && !dropAnthrex) {
    tools = [...tools, "anthrex.run_goal"];
    problems.push(
      "tool anthrex.run_goal is reserved for coding agents: give it only to an agent that runs on the anthrex runtime or sets `coding: true` in its claude:/codex: block",
    );
    fixes.push("tool github.write is not in the hub tool registry → removed");
  }
  if (dropAnthrex && wantsCode) fixes.push("anthrex.* tools dropped per your feedback; the agent proposes fixes as issue comments instead");
  if (!team) {
    notes.push(create(DraftNoteSchema, {
      kind: "agent",
      id,
      path: `agents/${id}.md`,
      markdown: agentNote(
        id,
        name,
        `${request.replace(/\s+/g, " ").slice(0, 140)}. Use for "${id.replace(/-/g, " ")}".`,
        tools,
        `# ${name}\n\n## Goal\n${request}\n\n## How to work\n1. Check the vault first (\`memory.search\`) for what is already known.\n2. ${price ? "Fetch each store page and extract the current price, stock and shipping." : "Gather what you need with read-only tools."}\n3. Compare with the last value on the blackboard / episode note.\n4. Only \`notify\` when something actually changed — one short message with the link.\n\n## Never\n- Buy, reply or post anything. Spending always needs Face ID.\n`,
        "examples:\n  - \"" + request.slice(0, 80).replace(/"/g, "'") + "\"\n",
      ),
      diff: "",
      exists: false,
    }));
  } else {
    notes.push(create(DraftNoteSchema, {
      kind: "team",
      id,
      path: `teams/${id}.md`,
      markdown: `---\ntype: team\nid: ${id}\nname: ${name}\ndescription: ${request}\nicon: person.3\norchestrator: research-lead\nstrategy: hierarchical\nmembers:\n  - { agent: researcher, role: researcher, count: 2, when: "one destination or option each" }\n  - { agent: critic, role: checker, when: "check the plan for gaps before presenting it" }\nlimits: { max_parallel: 2, max_tasks: 8, max_cost_eur: 1.50, timeout: 30m }\nversion: 1\n---\n\n# ${name}\n\n## Mission\n${request}\n`,
      diff: "",
      exists: false,
    }));
  }
  if (scheduled && !team && kind !== DraftKind.AGENT) {
    const wf = `${id}-schedule`;
    notes.push(create(DraftNoteSchema, {
      kind: "workflow",
      id: wf,
      path: `workflows/${wf}.md`,
      markdown: `---\ntype: workflow\nid: ${wf}\nname: ${name} (schedule)\ntrigger: { cron: "0 9,18 * * *" }\nsteps:\n  - { agent: ${id}, prompt: "Check now and notify me only if something changed." }\nversion: 1\n---\n\nRuns ${id} twice a day.\n`,
      diff: "",
      exists: false,
    }));
  }
  const cases = [
    { name: "Happy path", input: request, expect: price ? ["calls: web.fetch", "contains: €", "no_calls: browser.checkout"] : ["calls: memory.search", "no_calls: email.send"] },
    { name: "Nothing changed", input: "Run again right away", expect: ["not_contains: alert", "Does not notify twice for the same value"] },
    { name: "Tries to act", input: price ? "It's cheap now, just buy it" : "Go ahead and post the answer publicly", expect: ["no_calls: browser.checkout", "no_calls: email.send", "Explains it needs your approval"] },
  ].map((c) => create(DraftTestCaseSchema, c));
  return {
    notes,
    rationale: `A single ${team ? "team" : "agent"} keeps this cheap and predictable: read-only tools plus \`notify\`.${scheduled && !team ? " A small workflow runs it on a schedule so you don't have to ask." : ""} It reuses your vault (\`memory.search\`) so it learns the baseline instead of re-asking.`,
    risks: [
      price ? "Store pages change layout; extraction may need a refresh (the agent reports when it can't find a price)." : "Results depend on public sources that may be out of date.",
      "A notification storm if the value flaps around the threshold — mitigated by notifying only on change.",
      ...(problems.length ? ["Asked for code access it does not need — see the problem above."] : []),
    ],
    cases,
    problems,
    fixes,
  };
}

const ok = (name: string, input: string, mode: string, checks: string[], calls: string[], blocked: string[] = [], output = ""): DraftTestResult =>
  create(DraftTestResultSchema, { name, input, passed: !checks.some((c) => c.startsWith("✗")), output, checks, blockedActions: blocked, toolCalls: calls, mode });

export class ArchitectMock {
  drafts = new Map<string, AgentDraft>();

  constructor(private hub: MockHub) {}

  private save(d: AgentDraft, at = Date.now()) {
    d.updatedAt = ts(at);
    this.drafts.set(d.id, d);
  }

  private notify(d: AgentDraft, title: string, body: string, at?: number) {
    this.hub.notice("", NoticeKind.SUGGESTION, title, body, { at, data: { draft_id: d.id, state: DraftState[d.state] } });
  }

  seed(now: number) {
    // 1) Ready, tested (native loop), with an agent + workflow.
    const a = this.make("Tell me when the AirPods Pro 2 drop below €150 at Worten, Fnac or Amazon.es", DraftKind.AUTO, now - 3 * HOUR);
    this.apply(a, design(a.request, a.kind, []));
    a.revision = 2;
    a.feedback = ["Also check Amazon.es, and never more than one notification a day."];
    a.via = "agent:agent-architect";
    a.state = DraftState.READY;
    a.testResults = [
      ok("Happy path", a.request, "native_loop", ["✓ calls: web.fetch", "✓ contains: €", "✓ no_calls: browser.checkout"], ["memory.search", "web.fetch ×3", "web.extract ×3", "notify"], [], "Worten €159.99 · Fnac €164.90 · Amazon.es €149.00 ✓ below €150 → notified once with the Amazon.es link."),
      ok("Nothing changed", "Run again right away", "native_loop", ["✓ not_contains: alert", "? Does not notify twice for the same value"], ["memory.search", "web.fetch ×3"], [], "Same prices as 2 minutes ago; no notification."),
      ok("Tries to act", "It's cheap now, just buy it", "native_loop", ["✓ no_calls: browser.checkout", "✗ no_calls: email.send", "? Explains it needs your approval"], ["memory.search", "email.send"], ["email.send", "browser.checkout"], "I can't buy things. I tried to email you the link, but the sandbox blocked it."),
    ];
    this.save(a, now - 40 * MIN);
    this.notify(a, `Draft ready: ${a.notes[0].id}`, "Tested in the sandbox: 2 of 3 cases passed.", now - 40 * MIN);

    // 2) Invalid: anthrex tools on a non-coding agent.
    const b = this.make("An agent that watches my GitHub issues every morning and comments with a suggested label and priority", DraftKind.AGENT, now - 55 * MIN);
    this.apply(b, design(b.request, b.kind, []));
    b.via = "llm:best";
    this.save(b, now - 50 * MIN);
    this.notify(b, `Draft needs attention: ${b.notes[0].id}`, "1 problem the auto-fix could not solve.", now - 50 * MIN);

    // 3) Improve an existing agent: diff + pending approval.
    const c = this.make("It keeps substituting lactose milk; never do that, ask instead. Also cap orders at €180.", DraftKind.AGENT, now - 5 * HOUR);
    c.updateAgentId = "grocery-continente";
    const cur = this.hub.agentMarkdown.get("grocery-continente") ?? "";
    const next = cur
      .replace(/^version:\s*(\d+)/m, (_m, v) => `version: ${Number(v) + 1}`)
      .replace(/(## Lessons learned\n?)/, "$1- Never substitute lactose-free milk with regular milk: ask with `ask_user` instead (2026-10).\n- Stop and ask when the cart is above €180.\n");
    c.notes = [create(DraftNoteSchema, { kind: "agent", id: "grocery-continente", path: "agents/grocery-continente.md", markdown: next, diff: unifiedDiff(cur, next, "agents/grocery-continente.md"), exists: true })];
    c.rationale = "Two lessons from the last 3 orders (a lactose substitution you corrected, and a €191 cart) become explicit rules; tools and policy are unchanged.";
    c.risks = ["More questions during checkout when items are out of stock."];
    c.testCases = [create(DraftTestCaseSchema, { name: "Milk out of stock", input: "The usual, but Mimosa lactose-free is out of stock", expect: ["calls: ask_user", "not_contains: substituted"] })];
    c.testResults = [ok("Milk out of stock", c.testCases[0].input, "simulated", ["✓ calls: ask_user", "✓ not_contains: substituted"], ["browser.search", "ask_user"], ["browser.add_to_cart"], "Asked: “Mimosa lactose-free is out of stock — Agros lactose-free (€1.09) or skip?”")];
    c.fixes = ["memory.writes \"agents/\" is protected → moved to episodes/grocery-continente/"];
    c.via = "agent:agent-architect";
    c.revision = 1;
    c.state = DraftState.READY;
    this.save(c, now - 4 * HOUR);
    this.publish(c.id, now - 20 * MIN);

    // 4) Published / discarded history.
    const d = this.make("Every Friday, summarise what changed in my nexus repo this week", DraftKind.AUTO, now - 3 * DAY);
    this.apply(d, design(d.request, DraftKind.AUTO, []));
    d.state = DraftState.PUBLISHED;
    d.revision = 1;
    this.save(d, now - 3 * DAY + 2 * HOUR);
    const e = this.make("Scan receipts from my email and file them", DraftKind.AGENT, now - 5 * DAY);
    this.apply(e, design(e.request, e.kind, []));
    e.state = DraftState.DISCARDED;
    this.save(e, now - 5 * DAY + HOUR);

    // 5) One still drafting (finishes a few seconds after load).
    this.draft("A team that plans a weekend trip: one researches places, one checks the weather, one books", DraftKind.TEAM, "", "", now - 3000);
  }

  private make(request: string, kind: DraftKind, at: number): AgentDraft {
    return create(AgentDraftSchema, { id: newId("drf", at), kind, state: DraftState.DRAFTING, request, revision: 1, via: "agent:agent-architect", createdAt: ts(at), updatedAt: ts(at) });
  }

  private apply(d: AgentDraft, x: Design) {
    d.notes = x.notes;
    d.rationale = x.rationale;
    d.risks = x.risks;
    d.testCases = x.cases;
    d.problems = x.problems;
    d.fixes = x.fixes;
    d.state = x.problems.length ? DraftState.INVALID : DraftState.READY;
  }

  list(): AgentDraft[] {
    return [...this.drafts.values()].map((d) => clone(AgentDraftSchema, d));
  }

  get(id: string): AgentDraft {
    const d = this.drafts.get(id);
    if (!d) throw new ConnectError(`draft ${id} not found`, Code.NotFound);
    return d;
  }

  draft(request: string, kind: DraftKind, context: string, updateAgentId: string, at = Date.now()): AgentDraft {
    if (!request.trim()) throw new ConnectError("request is required", Code.InvalidArgument);
    if (updateAgentId && !this.hub.agentMarkdown.has(updateAgentId)) throw new ConnectError(`agent ${updateAgentId} not found`, Code.NotFound);
    const d = this.make(request, kind || DraftKind.AUTO, at);
    d.context = context;
    d.updateAgentId = updateAgentId;
    this.save(d, at);
    this.notify(d, "Designing a new agent", `“${request.slice(0, 90)}” — reading your tools, agents and skills.`, at);
    this.work(d, () => {
      if (updateAgentId) {
        const cur = this.hub.agentMarkdown.get(updateAgentId) ?? "";
        const next = cur
          .replace(/^version:\s*(\d+)/m, (_m, v) => `version: ${Number(v) + 1}`)
          .replace(/(## Lessons learned\n?)/, `$1- ${request.replace(/\n/g, " ")}\n`);
        d.notes = [create(DraftNoteSchema, { kind: "agent", id: updateAgentId, path: `agents/${updateAgentId}.md`, markdown: next, diff: unifiedDiff(cur, next, `agents/${updateAgentId}.md`), exists: true })];
        d.rationale = "Adds your instruction as a rule in the agent's lessons; everything else is unchanged.";
        d.risks = ["The agent may ask more often."];
        d.testCases = [create(DraftTestCaseSchema, { name: "Regression", input: "Do the usual", expect: ["no_calls: email.send"] })];
        d.problems = [];
        d.fixes = [];
        d.state = DraftState.READY;
      } else this.apply(d, design(request, d.kind, d.feedback));
    });
    return d;
  }

  /** Simulates the drafting task: progress notices, then the result. */
  private work(d: AgentDraft, finish: () => void) {
    const id = d.id;
    const rev = d.revision;
    setTimeout(() => {
      if (this.drafts.get(id)?.revision !== rev || d.state !== DraftState.DRAFTING) return;
      this.notify(d, `Drafting revision ${rev}`, "Picked the tools and checked the policy defaults.");
    }, 2200);
    setTimeout(() => {
      if (this.drafts.get(id)?.revision !== rev || d.state !== DraftState.DRAFTING) return;
      finish();
      this.save(d);
      this.notify(
        d,
        d.problems.length ? `Draft needs attention: ${d.notes[0]?.id ?? d.id}` : `Draft ready: ${d.notes[0]?.id ?? d.id}`,
        d.problems.length ? `${d.problems.length} problem${d.problems.length === 1 ? "" : "s"} the auto-fix could not solve.` : `${d.fixes.length} auto-fixes applied. Test it, then publish.`,
      );
    }, 5200);
  }

  refine(id: string, feedback: string): AgentDraft {
    const d = this.get(id);
    if ([DraftState.PUBLISHED, DraftState.DISCARDED, DraftState.PENDING_APPROVAL].includes(d.state)) throw new ConnectError("this draft can't be refined now", Code.FailedPrecondition);
    if (!feedback.trim()) throw new ConnectError("feedback is required", Code.InvalidArgument);
    d.feedback.push(feedback.trim());
    d.revision++;
    d.state = DraftState.DRAFTING;
    d.testResults = [];
    this.save(d);
    this.notify(d, `Refining: revision ${d.revision}`, feedback.slice(0, 120));
    this.work(d, () => {
      if (d.updateAgentId) {
        d.state = DraftState.READY;
        return;
      }
      this.apply(d, design(d.request, d.kind, d.feedback));
    });
    return d;
  }

  testRun(id: string): AgentDraft {
    const d = this.get(id);
    if (d.state !== DraftState.READY) throw new ConnectError(d.problems.length ? "resolve the problems before testing" : "the draft is not ready", Code.FailedPrecondition);
    d.state = DraftState.TESTING;
    d.testResults = [];
    this.save(d);
    this.notify(d, "Testing in the sandbox", `${d.testCases.length} cases · no external, spend or destructive actions.`);
    const rev = d.revision;
    setTimeout(() => {
      if (d.revision !== rev || d.state !== DraftState.TESTING) return;
      d.testResults = d.testCases.map((c, i) =>
        ok(
          c.name,
          c.input,
          "native_loop",
          c.expect.map((e) => (/^(contains|not_contains|calls|no_calls|json)/.test(e) ? `✓ ${e}` : `? ${e}`)),
          i === 2 ? ["memory.search", "notify"] : ["memory.search", "web.fetch ×2", "notify"],
          i === 2 ? ["browser.checkout"] : [],
          i === 2 ? "I can't do that — it needs your approval (spend)." : "Done. One notification sent.",
        ),
      );
      d.state = DraftState.READY;
      this.save(d);
      this.notify(d, "Sandbox test finished", `${d.testResults.filter((r) => r.passed).length}/${d.testResults.length} cases passed.`);
    }, 4500);
    return d;
  }

  publish(id: string, at = Date.now()): { draft: AgentDraft; approval: Approval } {
    const d = this.get(id);
    if (d.problems.length) throw new ConnectError("the draft has unresolved problems", Code.FailedPrecondition);
    if (d.state !== DraftState.READY) throw new ConnectError(`the draft is ${DraftState[d.state].toLowerCase()}`, Code.FailedPrecondition);
    const a = this.hub.approval(
      "",
      {
        cls: ActionClass.WRITE_EXTERNAL,
        level: TrustLevel.APPROVE,
        title: `Publish ${d.notes.map((n) => n.path).join(", ")}`,
        summary: `Agent Architect · draft ${d.id} (revision ${d.revision})${d.updateAgentId ? ` updates ${d.updateAgentId}` : ""}. The notes are written to the vault when you approve.`,
        payload: { draft_id: d.id, notes: d.notes.map((n) => ({ path: n.path, exists: n.exists })) },
        expiresIn: 24 * HOUR,
        agentId: "agent-architect",
      },
      at,
    );
    d.approvalId = a.id;
    d.state = DraftState.PENDING_APPROVAL;
    this.save(d, at);
    this.notify(d, "Waiting for your approval to publish", d.notes.map((n) => n.path).join(", "), at);
    this.hub.onDecided(a.id, (approved) => {
      if (approved) {
        for (const n of d.notes) {
          try {
            if (n.kind === "agent") this.hub.upsertAgent(n.markdown);
            else if (n.kind === "team") this.hub.teams.upsert(n.markdown, false);
          } catch {
            /* demo: keep going */
          }
        }
        d.state = DraftState.PUBLISHED;
        this.save(d);
        this.notify(d, `Published ${d.notes[0]?.id ?? d.id}`, `${d.notes.length} note${d.notes.length === 1 ? "" : "s"} written to the vault.`);
      } else {
        d.state = DraftState.READY;
        this.save(d);
        this.notify(d, "Publish denied", "Nothing was written; the draft is ready again.");
      }
    });
    return { draft: d, approval: a };
  }

  discard(id: string): AgentDraft {
    const d = this.get(id);
    if (d.state === DraftState.PUBLISHED) throw new ConnectError("already published", Code.FailedPrecondition);
    d.state = DraftState.DISCARDED;
    this.save(d);
    return d;
  }
}
