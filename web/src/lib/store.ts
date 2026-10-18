import { create } from "zustand";
import { create as createMsg, fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { CostRecordedSchema, EventSchema, TaskUpdatedSchema, type Event, type Notice, type Question } from "../gen/nexus/v1/event_pb";
import { ApprovalState, type Approval } from "../gen/nexus/v1/approval_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import type { NodeStatus } from "../gen/nexus/v1/node_pb";
import type { Cost } from "../gen/nexus/v1/common_pb";
import { tsMs } from "./format";
import { readJSON, removeKey, writeJSON } from "./storage";

export type ConnectionStatus = "idle" | "connecting" | "live" | "reconnecting" | "offline" | "unauthorized";

export interface Connection {
  status: ConnectionStatus;
  retryAt?: number;
  error?: string;
  attempt: number;
  /** When the stream last became live. */
  since?: number;
}

export interface QuestionItem {
  seq: bigint;
  at: number;
  taskId: string;
  actor: string;
  question: Question;
  answered: boolean;
}

export interface NoticeItem {
  seq: bigint;
  at: number;
  taskId: string;
  notice: Notice;
}

export interface CostItem {
  seq: bigint;
  at: number;
  taskId: string;
  cost: Cost;
}

export interface EventState {
  lastSeq: bigint;
  connection: Connection;
  tasks: Record<string, Task>;
  timelines: Record<string, Event[]>;
  approvals: Record<string, Approval>;
  questions: Record<string, QuestionItem>;
  notices: Record<string, NoticeItem>;
  dismissed: Record<string, true>;
  nodeStatus: Record<string, NodeStatus>;
  costs: CostItem[];
  /** Bumped on every ingest, handy for "something changed" effects. */
  version: number;
  /** True once the first batch after (re)connecting has been applied. */
  synced: boolean;

  ingest: (events: Event[], opts?: { global?: boolean }) => void;
  upsertTasks: (tasks: Task[]) => void;
  upsertApprovals: (approvals: Approval[]) => void;
  upsertNodeStatus: (statuses: NodeStatus[]) => void;
  /** Force-set a task (optimistic updates and their rollback); skips the updatedAt check. */
  putTask: (task: Task) => void;
  /** Force-set an approval (optimistic updates and their rollback). */
  putApproval: (approval: Approval) => void;
  setAnswered: (questionId: string, answered: boolean) => void;
  markAnswered: (questionId: string) => void;
  dismiss: (ids: string[]) => void;
  setConnection: (c: Partial<Connection>) => void;
  reset: () => void;
}

const TIMELINE_CAP = 4000;
const COST_CAP = 5000;
const WAITING_INPUT = new Set([TaskState.WAITING_INPUT, TaskState.NEEDS_INPUT]);

function newer(a: Task | undefined, b: Task): boolean {
  if (!a) return true;
  return tsMs(b.updatedAt) >= tsMs(a.updatedAt);
}

/** Highest seq an event stands for (coalesced output chunks cover a range). */
export function throughSeq(e: Event): bigint {
  return (e as Event & { $through?: bigint }).$through ?? e.seq;
}

/**
 * Merge a batch of events into a seq-ordered timeline, ignoring duplicates and events already
 * covered by a coalesced chunk. The common case (all new events are newer than the tail) is a
 * single concat; out-of-order history is merge-sorted. Returns null when nothing changed.
 */
function mergeTimeline(list: Event[] | undefined, add: Event[]): Event[] | null {
  if (add.length === 0) return null;
  if (add.length > 1) add.sort((a, b) => (a.seq < b.seq ? -1 : a.seq > b.seq ? 1 : 0));
  let next: Event[];
  if (!list || list.length === 0) {
    next = dedupeSorted(add);
  } else if (add[0].seq > throughSeq(list[list.length - 1])) {
    next = list.concat(dedupeSorted(add));
  } else {
    next = [];
    let i = 0;
    let j = 0;
    let added = 0;
    const pushB = (b: Event) => {
      const last = next[next.length - 1];
      if (last && b.seq <= throughSeq(last)) return; // duplicate or covered
      next.push(b);
      added++;
    };
    while (i < list.length || j < add.length) {
      const a = list[i];
      const b = add[j];
      if (b === undefined) {
        next.push(a);
        i++;
      } else if (a === undefined || b.seq < a.seq) {
        pushB(b);
        j++;
      } else if (b.seq <= throughSeq(a)) {
        j++; // already have it
      } else {
        next.push(a);
        i++;
      }
    }
    if (added === 0) return null;
  }
  return next.length > TIMELINE_CAP ? compact(next) : next;
}

function dedupeSorted(list: Event[]): Event[] {
  if (list.length < 2) return list;
  const out = [list[0]];
  for (let i = 1; i < list.length; i++) if (list[i].seq > throughSeq(out[out.length - 1])) out.push(list[i]);
  return out;
}

/**
 * Chunk coalescing: merge runs of consecutive output chunks of the same run into one event (keeping
 * the first seq, so the rendered block keeps its identity, and remembering the last seq it covers).
 * Long streams stay complete in a bounded array; only if that is not enough are the oldest dropped.
 */
function compact(list: Event[]): Event[] {
  const out: Event[] = [];
  for (const e of list) {
    const prev = out[out.length - 1];
    if (
      prev &&
      e.body.case === "runOutput" &&
      prev.body.case === "runOutput" &&
      prev.runId === e.runId &&
      !prev.body.value.isFinal
    ) {
      const merged = { ...prev, body: { case: "runOutput" as const, value: { ...prev.body.value, text: prev.body.value.text + e.body.value.text, isFinal: e.body.value.isFinal } } } as Event;
      Object.defineProperty(merged, "$through", { value: throughSeq(e), enumerable: false });
      out[out.length - 1] = merged;
    } else {
      out.push(e);
    }
  }
  return out.length > TIMELINE_CAP ? out.slice(out.length - TIMELINE_CAP) : out;
}

const initial = () => ({
  lastSeq: 0n,
  connection: { status: "idle" as ConnectionStatus, attempt: 0 },
  tasks: {} as Record<string, Task>,
  timelines: {} as Record<string, Event[]>,
  approvals: {} as Record<string, Approval>,
  questions: {} as Record<string, QuestionItem>,
  notices: {} as Record<string, NoticeItem>,
  dismissed: readJSON<Record<string, true>>("nexus.dismissed", {}),
  nodeStatus: {} as Record<string, NodeStatus>,
  costs: [] as CostItem[],
  version: 0,
  synced: false,
});

const costSeqs = new Set<bigint>();

export const useEvents = create<EventState>((set, get) => ({
  ...initial(),

  ingest: (events, opts) => {
    if (events.length === 0) return;
    const s = get();
    let { lastSeq } = s;
    let tasks = s.tasks;
    let timelines = s.timelines;
    let approvals = s.approvals;
    let questions = s.questions;
    let notices = s.notices;
    let nodeStatus = s.nodeStatus;
    let costs = s.costs;
    const touch = <T extends object>(cur: T, orig: T): T => (cur === orig ? { ...cur } : cur);
    const perTask = new Map<string, Event[]>();

    for (const ev of events) {
      if (opts?.global !== false && ev.seq > lastSeq) lastSeq = ev.seq;
      const at = tsMs(ev.at) || Date.now();

      if (ev.taskId && ev.seq > 0n) {
        const l = perTask.get(ev.taskId);
        if (l) l.push(ev);
        else perTask.set(ev.taskId, [ev]);
      }

      const b = ev.body;
      switch (b.case) {
        case "taskUpdated": {
          const t = b.value.task;
          if (t && newer(tasks[t.id], t)) {
            tasks = touch(tasks, s.tasks);
            tasks[t.id] = t;
            if (!WAITING_INPUT.has(t.state)) {
              for (const [qid, q] of Object.entries(questions)) {
                if (q.taskId === t.id && !q.answered && q.seq < ev.seq) {
                  questions = touch(questions, s.questions);
                  questions[qid] = { ...q, answered: true };
                }
              }
            }
          }
          break;
        }
        case "approval": {
          approvals = touch(approvals, s.approvals);
          approvals[b.value.id] = b.value;
          break;
        }
        case "question": {
          const qid = b.value.questionId || `q_${ev.seq}`;
          if (!questions[qid]) {
            questions = touch(questions, s.questions);
            const task = tasks[ev.taskId];
            const answered = task ? !WAITING_INPUT.has(task.state) && tsMs(task.updatedAt) > at : false;
            questions[qid] = { seq: ev.seq, at, taskId: ev.taskId, actor: ev.actor, question: b.value, answered };
          }
          break;
        }
        case "notice": {
          const id = b.value.id || `n_${ev.seq}`;
          notices = touch(notices, s.notices);
          notices[id] = { seq: ev.seq, at, taskId: ev.taskId, notice: b.value };
          break;
        }
        case "nodeStatus": {
          nodeStatus = touch(nodeStatus, s.nodeStatus);
          nodeStatus[b.value.nodeId] = b.value;
          break;
        }
        case "cost": {
          if (b.value.cost && !costSeqs.has(ev.seq)) {
            costSeqs.add(ev.seq);
            if (costs === s.costs) costs = costs.slice();
            costs.push({ seq: ev.seq, at, taskId: ev.taskId, cost: b.value.cost });
          }
          break;
        }
        default:
          break;
      }
    }
    for (const [taskId, add] of perTask) {
      const next = mergeTimeline(timelines[taskId], add);
      if (next) {
        timelines = touch(timelines, s.timelines);
        timelines[taskId] = next;
      }
    }
    if (costs.length > COST_CAP) costs = costs.slice(costs.length - COST_CAP);
    set({ lastSeq, tasks, timelines, approvals, questions, notices, nodeStatus, costs, version: s.version + 1, synced: true });
    scheduleSnapshot();
  },

  upsertTasks: (list) => {
    const s = get();
    let tasks = s.tasks;
    for (const t of list) {
      if (newer(tasks[t.id], t)) {
        if (tasks === s.tasks) tasks = { ...tasks };
        tasks[t.id] = t;
      }
    }
    if (tasks !== s.tasks) set({ tasks });
  },

  upsertApprovals: (list) => {
    if (list.length === 0) return;
    const approvals = { ...get().approvals };
    for (const a of list) approvals[a.id] = a;
    set({ approvals });
  },

  upsertNodeStatus: (list) => {
    const nodeStatus = { ...get().nodeStatus };
    let changed = false;
    for (const st of list) {
      const cur = nodeStatus[st.nodeId];
      if (!cur || tsMs(st.lastSeen) >= tsMs(cur.lastSeen)) {
        nodeStatus[st.nodeId] = st;
        changed = true;
      }
    }
    if (changed) set({ nodeStatus });
  },

  putTask: (t) => set({ tasks: { ...get().tasks, [t.id]: t } }),

  putApproval: (a) => set({ approvals: { ...get().approvals, [a.id]: a } }),

  setAnswered: (qid, answered) => {
    const q = get().questions[qid];
    if (!q || q.answered === answered) return;
    set({ questions: { ...get().questions, [qid]: { ...q, answered } } });
  },

  markAnswered: (qid) => {
    const q = get().questions[qid];
    if (!q) return;
    set({ questions: { ...get().questions, [qid]: { ...q, answered: true } } });
  },

  dismiss: (ids) => {
    const dismissed = { ...get().dismissed };
    for (const id of ids) dismissed[id] = true;
    writeJSON("nexus.dismissed", dismissed);
    set({ dismissed });
  },

  setConnection: (c) => set({ connection: { ...get().connection, ...c } }),

  reset: () => {
    costSeqs.clear();
    set({ ...initial(), connection: get().connection });
  },
}));

// ---- snapshot persistence ----------------------------------------------------------------------
// The stream resumes from the stored seq; the snapshot restores the derived state (tasks, inbox,
// nodes, costs) that the replay will not resend. Full timelines are re-fetched per task on demand.

let snapshotKey: string | null = null;
let snapshotTimer: ReturnType<typeof setTimeout> | undefined;

interface Snapshot {
  v: 1;
  lastSeq: string;
  events: JsonValue[];
}

function buildSnapshot(): Snapshot {
  const s = useEvents.getState();
  const events: Event[] = [];
  const pickLatest = (taskId: string, pred: (e: Event) => boolean) => {
    const tl = s.timelines[taskId];
    if (!tl) return undefined;
    for (let i = tl.length - 1; i >= 0; i--) if (pred(tl[i])) return tl[i];
    return undefined;
  };
  const tasks = Object.values(s.tasks)
    .sort((a, b) => tsMs(b.updatedAt) - tsMs(a.updatedAt))
    .slice(0, 300);
  for (const t of tasks) {
    const ev =
      pickLatest(t.id, (e) => e.body.case === "taskUpdated" && e.body.value.task?.id === t.id) ??
      synth(0n, t.id, { case: "taskUpdated", value: createMsg(TaskUpdatedSchema, { task: t }) });
    events.push(ev);
  }
  for (const a of Object.values(s.approvals)) events.push(synth(0n, a.taskId, { case: "approval", value: a }));
  for (const q of Object.values(s.questions)) {
    if (!q.answered) events.push(synth(q.seq, q.taskId, { case: "question", value: q.question }, q.at));
  }
  for (const n of Object.values(s.notices)) {
    if (!s.dismissed[n.notice.id]) events.push(synth(n.seq, n.taskId, { case: "notice", value: n.notice }, n.at));
  }
  for (const st of Object.values(s.nodeStatus)) events.push(synth(0n, "", { case: "nodeStatus", value: st }));
  for (const c of s.costs) events.push(synth(c.seq, c.taskId, { case: "cost", value: createMsg(CostRecordedSchema, { cost: c.cost }) }, c.at));
  return { v: 1, lastSeq: s.lastSeq.toString(), events: events.map((e) => toJson(EventSchema, e)) };
}

function synth(seq: bigint, taskId: string, body: Event["body"], at?: number): Event {
  return createMsg(EventSchema, {
    seq,
    taskId,
    actor: "snapshot",
    at: at ? timestampFromMs(at) : undefined,
    body,
  });
}

function scheduleSnapshot() {
  if (!snapshotKey || snapshotTimer) return;
  snapshotTimer = setTimeout(() => {
    snapshotTimer = undefined;
    if (snapshotKey) writeJSON(snapshotKey, buildSnapshot());
  }, 3000);
}

/** Enable persistence under a key bound to the hub, and restore any saved snapshot. Returns the resume seq. */
export function restoreSnapshot(key: string | null): bigint {
  snapshotKey = key;
  if (!key) return 0n;
  const snap = readJSON<Snapshot | null>(key, null);
  if (!snap || snap.v !== 1) return 0n;
  try {
    const events = snap.events.map((j) => fromJson(EventSchema, j, { ignoreUnknownFields: true }));
    // Snapshot events carry seq 0 for "latest state" records; ingest without moving lastSeq.
    useEvents.getState().ingest(events, { global: false });
    const seq = BigInt(snap.lastSeq);
    useEvents.setState({ lastSeq: seq });
    // Snapshot timelines are partial; drop them so task pages fetch the full history.
    useEvents.setState({ timelines: {} });
    return seq;
  } catch {
    removeKey(key);
    return 0n;
  }
}

export function clearSnapshot(key: string | null) {
  if (key) removeKey(key);
}

// ---- convenience accessors ---------------------------------------------------------------------

export function isPendingApproval(a: Approval, now = Date.now()): boolean {
  if (a.state !== ApprovalState.PENDING) return false;
  const exp = tsMs(a.expiresAt);
  return !exp || exp > now;
}
