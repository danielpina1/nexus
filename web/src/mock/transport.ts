import { createRouterTransport, type Interceptor, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { AgentService } from "../gen/nexus/v1/agent_pb";
import { ApprovalService } from "../gen/nexus/v1/approval_pb";
import { CommandKind, CommandService } from "../gen/nexus/v1/command_pb";
import { DeviceService, PairingKind } from "../gen/nexus/v1/device_pb";
import { EventService } from "../gen/nexus/v1/event_pb";
import { NodeService } from "../gen/nexus/v1/node_pb";
import { TaskService, type Task } from "../gen/nexus/v1/task_pb";
import { FactStatus, NoteSchema, VaultService } from "../gen/nexus/v1/vault_pb";
import { MockHub, searchNotes } from "./hub";
import { listFiles } from "./fixtures";
import { backlinksOf, findNote, linksOf, notes, upsertNote, type MockNote } from "./vaultData";
import { latency, MIN, randomBytes, ts } from "./util";
import { tsMs } from "../lib/format";
import { NoticeKind } from "../gen/nexus/v1/event_pb";

let hub: MockHub | null = null;

/** Stable fake Hub CA fingerprint (SHA-256) for mock pairing codes. */
const MOCK_CA = randomBytes(32);

const ref = (n: MockNote) => ({ id: n.id, path: n.path, title: n.title });

function noteMessage(n: MockNote) {
  return create(NoteSchema, {
    ref: ref(n),
    type: n.type,
    tags: n.tags,
    frontmatter: n.frontmatter as never,
    body: n.body,
    links: linksOf(n).map(ref),
    backlinks: backlinksOf(n).map(ref),
    updatedAt: ts(n.updatedAt),
    commit: n.updatedAt.toString(16).slice(-7),
  });
}

/**
 * An in-memory Nexus Hub implementing every client-facing service, used when VITE_MOCK=1 or the
 * Settings toggle is on. It replays ~2 weeks of history and keeps a live simulation running.
 */
export function createMockTransport(interceptors: Interceptor[] = []) {
  hub ??= new MockHub();
  const h = hub;
  return createRouterTransport(
    ({ service }) => {
      service(EventService, {
        async *subscribe(req, ctx) {
          for await (const event of h.subscribe(req.sinceSeq, req.taskIds, req.inboxOnly, ctx.signal)) {
            yield { event };
          }
        },
      });

      service(TaskService, {
        async createTask(req) {
          await latency();
          if (!req.prompt.trim()) throw new ConnectError("prompt is required", Code.InvalidArgument);
          const route = h.route(req.prompt);
          return { task: h.runGeneric(req.prompt, req.agentId || route.agentId || "assistant", { source: req.source || "web" }) };
        },
        async getTask(req) {
          await latency();
          const task = h.tasks.get(req.id);
          if (!task) throw new ConnectError(`task ${req.id} not found`, Code.NotFound);
          const children = req.includeChildren ? [...h.tasks.values()].filter((t) => t.parentId === req.id) : [];
          return { task, children };
        },
        async listTasks(req) {
          await latency();
          let list: Task[] = [...h.tasks.values()];
          if (req.states.length) list = list.filter((t) => req.states.includes(t.state));
          if (req.agentId) list = list.filter((t) => t.agentId === req.agentId);
          if (req.nodeId) list = list.filter((t) => t.nodeId === req.nodeId);
          if (req.rootsOnly) list = list.filter((t) => !t.parentId);
          list.sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt));
          const size = req.page?.pageSize || 50;
          const start = Number(req.page?.pageToken || 0);
          const page = list.slice(start, start + size);
          return { tasks: page, nextPageToken: start + size < list.length ? String(start + size) : "" };
        },
        async sendMessage(req) {
          await latency();
          h.sendMessage(req.taskId, req.text, req.questionId, req.choice);
          return {};
        },
        async cancelTask(req) {
          await latency();
          h.cancel(req.id);
          return {};
        },
        async escalateTask(req) {
          await latency();
          return { task: h.escalate(req.id, req.hint) };
        },
        async stopAll() {
          await latency();
          return { cancelled: h.stopAll() };
        },
        async followTask() {
          await latency();
          return {};
        },
      });

      service(ApprovalService, {
        async listApprovals(req) {
          await latency();
          const all = [...h.approvals.values()];
          return { approvals: req.states.length ? all.filter((a) => req.states.includes(a.state)) : all };
        },
        async decide(req) {
          await latency();
          const d = req.decision;
          if (!d) throw new ConnectError("decision is required", Code.InvalidArgument);
          return { approval: h.decide(d.approvalId, d.approve, d.signature, d.deviceId) };
        },
      });

      service(CommandService, {
        async plan(req) {
          await latency();
          return { route: h.route(req.input) };
        },
        async execute(req) {
          await latency();
          const forced = req.route?.agentId;
          const route = forced ? { ...h.route(req.input), agentId: forced, kind: CommandKind.TASK, confidence: 1, reason: "Chosen by you" } : h.route(req.input);
          if (route.kind === CommandKind.CAPTURE) {
            const note = h.capture(req.input, req.context?.source ?? "web");
            return { route, answer: `Captured to ${note.path}. The Librarian will file and link it.`, sources: [note] };
          }
          if (route.kind === CommandKind.SEARCH) {
            const hits = searchNotes(req.input, 3);
            const answer = hits.length
              ? `From your vault: ${hits[0].snippet.replace(/[#*]/g, "").trim()}`
              : "I couldn't find anything about that in your vault. Want me to ask the Assistant to look into it?";
            return { route, answer, sources: hits.map((x) => ({ id: x.id, path: x.path, title: x.title })) };
          }
          if (!route.agentId) {
            return {
              route,
              question: "I'm not sure who should handle that. Pick an agent:",
              choices: route.alternatives,
            };
          }
          const task = h.runGeneric(req.input, route.agentId, { source: "web" });
          return { route, task };
        },
        async correctRoute(req) {
          await latency();
          return { task: h.tasks.get(req.taskId) };
        },
      });

      service(AgentService, {
        async listAgents() {
          await latency();
          return { agents: h.listAgents() };
        },
        async getAgent(req) {
          await latency();
          const md = h.agentMarkdown.get(req.id);
          if (!md) throw new ConnectError(`agent ${req.id} not found`, Code.NotFound);
          return { agent: h.listAgents().find((a) => a.id === req.id), markdown: md };
        },
        async upsertAgent(req) {
          await latency();
          return { agent: h.upsertAgent(req.markdown) };
        },
      });

      service(NodeService, {
        async listNodes() {
          await latency();
          return { nodes: h.nodes };
        },
        async getNode(req) {
          await latency();
          const node = h.nodes.find((n) => n.id === req.id);
          if (!node) throw new ConnectError(`node ${req.id} not found`, Code.NotFound);
          return { node };
        },
        async listFiles(req) {
          await latency();
          if (!h.nodes.some((n) => n.id === req.nodeId)) throw new ConnectError("node not found", Code.NotFound);
          return { entries: listFiles(req.nodeId, req.path, Date.now()) };
        },
        async wakeNode(req) {
          await latency();
          h.notice("", NoticeKind.RESULT, `Wake-on-LAN sent to ${req.id}`, "Magic packet sent via vps.");
          return {};
        },
      });

      service(DeviceService, {
        async listDevices() {
          await latency();
          return { devices: h.devices };
        },
        async revokeDevice(req) {
          await latency();
          const d = h.devices.find((x) => x.id === req.id);
          if (!d) throw new ConnectError("device not found", Code.NotFound);
          if (d.primary) throw new ConnectError("The primary device holds the owner key and cannot be revoked here.", Code.FailedPrecondition);
          h.devices = h.devices.filter((x) => x.id !== req.id);
          h.notice("", NoticeKind.ALERT, `Device revoked: ${d.name}`, "Its keys were removed from every node.");
          return {};
        },
        async pair() {
          throw new ConnectError("Pairing happens from the iOS app by scanning the QR code.", Code.Unimplemented);
        },
        async createPairing(req) {
          await latency();
          const kind = req.kind === PairingKind.NODE ? PairingKind.NODE : PairingKind.DEVICE;
          const b64url = (b: Uint8Array) => btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
          const hex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
          const hubUrls = ["https://nexus.home.arpa:7700"];
          const caFingerprint = hex(MOCK_CA);
          const hubKey = new Uint8Array(65);
          hubKey[0] = 4;
          hubKey.set(randomBytes(64), 1);
          const exp = Date.now() + 5 * MIN;
          const q = new URLSearchParams({ v: "1", k: kind === PairingKind.NODE ? "node" : "device" });
          for (const u of hubUrls) q.append("hub", u);
          q.set("ca", caFingerprint);
          q.set("s", b64url(randomBytes(32)));
          q.set("hk", b64url(hubKey));
          q.set("exp", String(Math.floor(exp / 1000)));
          h.notice("", NoticeKind.ALERT, "Pairing enabled", `A ${kind === PairingKind.NODE ? "node" : "device"} pairing code was issued (valid 5 minutes).`);
          return { url: `nexus://pair?${q.toString()}`, expiresAt: ts(exp), kind, hubUrls, caFingerprint };
        },
        async pairNode() {
          throw new ConnectError("Node pairing needs a real Hub (nexus-node pair).", Code.Unimplemented);
        },
        async getChallenge() {
          throw new ConnectError("not available in mock mode", Code.Unimplemented);
        },
        async authenticate() {
          throw new ConnectError("not available in mock mode", Code.Unimplemented);
        },
        async registerPushToken() {
          return {};
        },
        async registerLiveActivityToken() {
          return {};
        },
      });

      service(VaultService, {
        async search(req) {
          await latency();
          let hits = searchNotes(req.query, req.limit || 20);
          if (req.types.length) hits = hits.filter((x) => req.types.includes(findNote(x.id)?.type ?? ""));
          return { hits: hits.map((x) => ({ note: { id: x.id, path: x.path, title: x.title }, snippet: x.snippet, score: x.score })) };
        },
        async ask(req) {
          await latency();
          const hits = searchNotes(req.question, 3);
          return { answer: hits.length ? hits[0].snippet : "Nothing in the vault about that.", sources: hits.map((x) => ({ id: x.id, path: x.path, title: x.title })) };
        },
        async getNote(req) {
          await latency();
          const n = findNote(req.id || req.path);
          if (!n) throw new ConnectError(`note "${req.id || req.path}" not found`, Code.NotFound);
          return { note: noteMessage(n) };
        },
        async writeNote(req) {
          await latency();
          return { note: noteMessage(upsertNote(req.path, req.markdown)) };
        },
        async capture(req) {
          await latency();
          return { note: h.capture(req.text || req.url, req.source || "web") };
        },
        async graph(req) {
          await latency();
          let set = notes.filter((n) => !n.path.startsWith("templates/"));
          if (req.types.length) set = set.filter((n) => req.types.includes(n.type));
          if (req.centerId) {
            const center = findNote(req.centerId);
            if (center) {
              const keep = new Set([center.id]);
              let frontier = [center];
              for (let d = 0; d < (req.depth || 1); d++) {
                const next: MockNote[] = [];
                for (const n of frontier) for (const m of [...linksOf(n), ...backlinksOf(n)]) if (!keep.has(m.id)) { keep.add(m.id); next.push(m); }
                frontier = next;
              }
              set = set.filter((n) => keep.has(n.id));
            }
          }
          const ids = new Set(set.map((n) => n.id));
          const edges = set.flatMap((n) => linksOf(n).filter((m) => ids.has(m.id)).map((m) => ({ sourceId: n.id, targetId: m.id, relation: "links_to" })));
          return { nodes: set.map((n) => ({ ...ref(n), path: n.path })), edges };
        },
        async listFacts(req) {
          await latency();
          return { facts: h.facts.filter((f) => !req.status || f.status === req.status) };
        },
        async reviewFact(req) {
          await latency();
          const f = h.facts.find((x) => x.id === req.id);
          if (!f) throw new ConnectError("fact not found", Code.NotFound);
          f.status = req.status || FactStatus.CONFIRMED;
          if (req.correctedObject) f.object = req.correctedObject;
          return { fact: f };
        },
      });
    },
    { transport: { interceptors } },
  );
}
