import type { Event, ToolCall, ToolResult } from "../gen/nexus/v1/event_pb";
import type { Attachment } from "../gen/nexus/v1/common_pb";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { tsMs } from "./format";

/** A run's timeline grouped into displayable items (used by RunView and the chat bubble). */
export type Item =
  | { k: "text"; key: string; text: string; streaming: boolean }
  | { k: "tool"; key: string; call: ToolCall; result?: ToolResult; at: number }
  | { k: "state"; key: string; state: TaskState; at: number }
  | { k: "question"; key: string; qid: string; ev: Event }
  | { k: "approval"; key: string; id: string }
  | { k: "artifact"; key: string; att: Attachment }
  | { k: "user"; key: string; text: string; at: number };

export type RunItem = Item;

export function buildItems(events: Event[], taskId: string): Item[] {
  const items: Item[] = [];
  const tools = new Map<string, Extract<Item, { k: "tool" }>>();
  const seenApprovals = new Set<string>();
  let lastState: TaskState | undefined;
  let text: Extract<Item, { k: "text" }> | null = null;
  for (const e of events) {
    const b = e.body;
    switch (b.case) {
      case "runOutput":
        if (!text) {
          text = { k: "text", key: `t${e.seq}`, text: "", streaming: true };
          items.push(text);
        }
        text.text += b.value.text;
        if (b.value.isFinal) text.streaming = false;
        break;
      case "toolCall": {
        const it: Extract<Item, { k: "tool" }> = { k: "tool", key: `c${e.seq}`, call: b.value, at: tsMs(e.at) };
        tools.set(b.value.callId, it);
        items.push(it);
        break;
      }
      case "toolResult": {
        const it = tools.get(b.value.callId);
        if (it) it.result = b.value;
        break;
      }
      case "taskUpdated": {
        const t = b.value.task;
        if (t && t.id === taskId && t.state !== lastState) {
          // Only show meaningful transitions.
          if (lastState !== undefined && t.state !== TaskState.ASSIGNED && t.state !== TaskState.ROUTING) {
            items.push({ k: "state", key: `s${e.seq}`, state: t.state, at: tsMs(e.at) });
          }
          lastState = t.state;
        }
        break;
      }
      case "question":
        items.push({ k: "question", key: `q${e.seq}`, qid: b.value.questionId || `q_${e.seq}`, ev: e });
        break;
      case "approval":
        if (!seenApprovals.has(b.value.id)) {
          seenApprovals.add(b.value.id);
          items.push({ k: "approval", key: `a${e.seq}`, id: b.value.id });
        }
        break;
      case "artifact":
        if (b.value.attachment) items.push({ k: "artifact", key: `f${e.seq}`, att: b.value.attachment });
        break;
      case "userMessage":
        // Answers to questions arrive as choices (shown on the question card), not text.
        if (b.value.text.trim()) items.push({ k: "user", key: `u${e.seq}`, text: b.value.text, at: tsMs(e.at) });
        break;
    }
    // A new non-text item ends the current text block; costs etc. don't.
    if (text && items[items.length - 1] !== text) text = null;
  }
  return items;
}


/** True when the streamed text already contains the result summary (do not show it twice). */
export function summaryShown(items: Item[], summary: string): boolean {
  const s = summary.trim().slice(0, 120);
  return !!s && items.some((it) => it.k === "text" && it.text.includes(s));
}

