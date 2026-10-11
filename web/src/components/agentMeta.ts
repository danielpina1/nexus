import { useMemo } from "react";
import { useAgents } from "../lib/queries";
import type { Agent } from "../gen/nexus/v1/agent_pb";

/** Fixed categorical slot per known agent so a color always means the same agent. */
const SLOT: Record<string, number> = {
  assistant: 1,
  "grocery-continente": 2,
  librarian: 3,
  ops: 4,
  "code-fixer": 7,
};

export function agentSlot(agentId: string): number {
  if (SLOT[agentId]) return SLOT[agentId];
  let h = 0;
  for (const c of agentId) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return [5, 6, 8][h % 3];
}

export function agentColor(agentId: string): string {
  return `var(--series-${agentSlot(agentId)})`;
}

export function useAgentMap(): Record<string, Agent> {
  const { data } = useAgents();
  return useMemo(() => Object.fromEntries((data ?? []).map((a) => [a.id, a])), [data]);
}

export function agentName(map: Record<string, Agent>, id: string, workflowId?: string): string {
  if (!id) return workflowId ? `Workflow · ${workflowId}` : "Nexus";
  return map[id]?.name ?? id;
}
