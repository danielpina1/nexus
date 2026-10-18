import { useMemo } from "react";
import { useEvents, isPendingApproval, type NoticeItem, type QuestionItem } from "./store";
import type { Approval } from "../gen/nexus/v1/approval_pb";
import { TrustLevel } from "../gen/nexus/v1/common_pb";
import { NoticeKind, type Event } from "../gen/nexus/v1/event_pb";
import { TaskState, type Task } from "../gen/nexus/v1/task_pb";
import { isActive, tsMs } from "./format";
import { useNow } from "./useNow";

export type InboxItem =
  | { kind: "approval"; id: string; at: number; rank: number; approval: Approval }
  | { kind: "question"; id: string; at: number; rank: number; item: QuestionItem }
  | { kind: "notice"; id: string; at: number; rank: number; item: NoticeItem };

function noticeRank(k: NoticeKind): number {
  switch (k) {
    case NoticeKind.ALERT:
      return 3;
    case NoticeKind.RESULT:
    case NoticeKind.BRIEF:
      return 4;
    case NoticeKind.FACT_REVIEW:
      return 5;
    case NoticeKind.SUGGESTION:
      return 6;
    default:
      return 4;
  }
}

/** The ranked inbox: approvals → questions → alerts → results → facts → suggestions. */
export function useInbox(): InboxItem[] {
  const approvals = useEvents((s) => s.approvals);
  const questions = useEvents((s) => s.questions);
  const notices = useEvents((s) => s.notices);
  const dismissed = useEvents((s) => s.dismissed);
  const tasks = useEvents((s) => s.tasks);
  const now = useNow(15_000);
  return useMemo(() => {
    const items: InboxItem[] = [];
    for (const a of Object.values(approvals)) {
      if (!isPendingApproval(a, now)) continue;
      items.push({ kind: "approval", id: a.id, at: tsMs(a.createdAt), rank: a.level === TrustLevel.BIOMETRIC ? 0 : 1, approval: a });
    }
    for (const [id, q] of Object.entries(questions)) {
      if (q.answered || dismissed[id]) continue;
      const t = tasks[q.taskId];
      if (t && !isActive(t.state)) continue;
      items.push({ kind: "question", id, at: q.at, rank: 2, item: q });
    }
    for (const [id, n] of Object.entries(notices)) {
      if (dismissed[id]) continue;
      items.push({ kind: "notice", id, at: n.at, rank: noticeRank(n.notice.kind), item: n });
    }
    // Failed tasks surface as alerts too.
    return items.sort((a, b) => a.rank - b.rank || b.at - a.at);
  }, [approvals, questions, notices, dismissed, tasks, now]);
}

export function useInboxCount(): number {
  return useInbox().length;
}

/** Number of active tasks; re-renders only when the count changes. */
export function useRunningCount(): number {
  return useEvents((s) => {
    let n = 0;
    for (const id in s.tasks) if (isActive(s.tasks[id].state)) n++;
    return n;
  });
}

export function useRunningTasks(): Task[] {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(
    () =>
      Object.values(tasks)
        .filter((t) => isActive(t.state))
        .sort((a, b) => stateWeight(a.state) - stateWeight(b.state) || tsMs(b.createdAt) - tsMs(a.createdAt)),
    [tasks],
  );
}

function stateWeight(s: TaskState): number {
  if (s === TaskState.WAITING_APPROVAL || s === TaskState.WAITING_INPUT || s === TaskState.NEEDS_INPUT) return 0;
  if (s === TaskState.RUNNING) return 1;
  return 2;
}

export function useAllTasks(): Task[] {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(() => Object.values(tasks).sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt)), [tasks]);
}

const EMPTY: Event[] = [];
export function useTimeline(taskId: string | undefined): Event[] {
  return useEvents((s) => (taskId ? (s.timelines[taskId] ?? EMPTY) : EMPTY));
}

export function useTask(taskId: string | undefined): Task | undefined {
  return useEvents((s) => (taskId ? s.tasks[taskId] : undefined));
}

export function useChildren(taskId: string | undefined): Task[] {
  const tasks = useEvents((s) => s.tasks);
  return useMemo(
    () =>
      taskId
        ? Object.values(tasks)
            .filter((t) => t.parentId === taskId)
            .sort((a, b) => tsMs(a.createdAt) - tsMs(b.createdAt))
        : [],
    [tasks, taskId],
  );
}

/** Summary of a run derived from its timeline: streamed text, tool calls paired with results, cost. */
export interface RunSummary {
  output: string;
  lastLine: string;
  costMinor: number;
  currency: string;
  inputTokens: number;
  outputTokens: number;
  toolCalls: number;
  final: boolean;
}

export function summarizeTimeline(events: Event[]): RunSummary {
  let output = "";
  let costMinor = 0;
  let currency = "EUR";
  let inputTokens = 0;
  let outputTokens = 0;
  let toolCalls = 0;
  let final = false;
  for (const e of events) {
    switch (e.body.case) {
      case "runOutput":
        output += e.body.value.text;
        if (e.body.value.isFinal) final = true;
        break;
      case "cost": {
        const c = e.body.value.cost;
        if (c) {
          costMinor += Number(c.amount?.minorUnits ?? 0n);
          currency = c.amount?.currency || currency;
          inputTokens += Number(c.inputTokens);
          outputTokens += Number(c.outputTokens);
        }
        break;
      }
      case "toolCall":
        toolCalls++;
        break;
    }
  }
  const lines = output.trimEnd().split("\n");
  return { output, lastLine: lines[lines.length - 1] ?? "", costMinor, currency, inputTokens, outputTokens, toolCalls, final };
}

export function useRunSummary(taskId: string | undefined): RunSummary {
  const tl = useTimeline(taskId);
  return useMemo(() => summarizeTimeline(tl), [tl]);
}
