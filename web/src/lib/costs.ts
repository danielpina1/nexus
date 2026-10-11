import { useMemo } from "react";
import { useEvents, type CostItem } from "./store";
import { isLocalRuntime } from "./format";
import type { Task } from "../gen/nexus/v1/task_pb";

export function startOfDay(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export interface CostRow extends CostItem {
  agentId: string;
  local: boolean;
  minor: number;
}

/** Cost events joined with their task's agent. */
export function useCostRows(): CostRow[] {
  const costs = useEvents((s) => s.costs);
  const tasks = useEvents((s) => s.tasks);
  return useMemo(() => enrich(costs, tasks), [costs, tasks]);
}

function enrich(costs: CostItem[], tasks: Record<string, Task>): CostRow[] {
  return costs.map((c) => ({
    ...c,
    agentId: tasks[c.taskId]?.agentId || "unknown",
    local: isLocalRuntime(c.cost.runtime, c.cost.model),
    minor: Number(c.cost.amount?.minorUnits ?? 0n),
  }));
}

export function useTodayCost() {
  const rows = useCostRows();
  return useMemo(() => {
    const t0 = startOfDay(Date.now());
    let cloud = 0;
    let localTokens = 0;
    let cloudTokens = 0;
    let localRuns = 0;
    let currency = "EUR";
    for (const r of rows) {
      if (r.at < t0) continue;
      currency = r.cost.amount?.currency || currency;
      const tok = Number(r.cost.inputTokens + r.cost.outputTokens);
      if (r.local) {
        localTokens += tok;
        localRuns++;
      } else {
        cloud += r.minor;
        cloudTokens += tok;
      }
    }
    return { cloud, localTokens, cloudTokens, localRuns, currency };
  }, [rows]);
}

export function useDailyCosts(days: number) {
  const rows = useCostRows();
  return useMemo(() => {
    const today = startOfDay(Date.now());
    const out = Array.from({ length: days }, (_, i) => ({ day: today - (days - 1 - i) * 86_400_000, minor: 0 }));
    for (const r of rows) {
      const idx = Math.round((startOfDay(r.at) - out[0].day) / 86_400_000);
      if (idx >= 0 && idx < days) out[idx].minor += r.minor;
    }
    return out;
  }, [rows, days]);
}
