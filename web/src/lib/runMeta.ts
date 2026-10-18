import { useMemo } from "react";
import type { Event } from "../gen/nexus/v1/event_pb";
import type { Task } from "../gen/nexus/v1/task_pb";
import { useTimeline } from "./selectors";

/** Display names of runtimes ("engines"). */
const ENGINES: Record<string, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  openai: "OpenAI",
  ollama: "Ollama",
  anthrex: "Anthrex",
  shell: "Shell",
  browser: "Browser",
  echo: "Echo",
  workflow: "Workflow",
};

export function engineLabel(runtime: string | undefined): string {
  if (!runtime) return "";
  return ENGINES[runtime] ?? runtime;
}

export interface RunModel {
  /** Runtime id ("claude-code"), "" when unknown yet. */
  runtime: string;
  /** Exact model id reported by the CLI ("claude-sonnet-4-5-20250929"), "" = CLI default / unknown. */
  model: string;
  /** True once a cost event named the runtime (the run started on a machine). */
  known: boolean;
}

/**
 * The engine and exact model that served the run, from the latest cost event that names them
 * (runtimes announce it at start with a zero cost and repeat it in the final cost; a runtime
 * fallback announces the new one), else the task's accumulated cost.
 */
export function runModelFrom(events: Event[], task?: Task): RunModel {
  for (let i = events.length - 1; i >= 0; i--) {
    const b = events[i].body;
    if (b.case === "cost" && b.value.cost?.runtime) {
      return { runtime: b.value.cost.runtime, model: b.value.cost.model, known: true };
    }
  }
  if (task?.cost?.runtime) return { runtime: task.cost.runtime, model: task.cost.model, known: true };
  return { runtime: "", model: task?.cost?.model ?? "", known: false };
}

export function useRunModel(task: Task | undefined): RunModel {
  const events = useTimeline(task?.id);
  return useMemo(() => runModelFrom(events, task), [events, task]);
}

/** "claude-sonnet-4-5-20250929", or "Codex default" when the CLI picked its own model. */
export function modelLabel(m: RunModel): string {
  if (m.model) return m.model;
  if (m.runtime === "codex") return "Codex default";
  return m.known ? "default model" : "";
}

/** "Claude Code · claude-sonnet-4-5-20250929". */
export function engineModelLabel(m: RunModel): string {
  return [engineLabel(m.runtime), modelLabel(m)].filter(Boolean).join(" · ");
}

export { claimsBackgroundWork, untrackedBackgroundWarning, BACKGROUND_WARNING } from "./backgroundClaim";
