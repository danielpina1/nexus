import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { api } from "./api";
import { useEvents } from "./store";
import { toast } from "./toast";
import { ApprovalState, type Approval } from "../gen/nexus/v1/approval_pb";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { create } from "zustand";
import { useSettings } from "./settings";

export { queryClient } from "./queryClient";

export const qk = {
  agents: ["agents"] as const,
  agent: (id: string) => ["agent", id] as const,
  tasks: (f: object) => ["tasks", f] as const,
  task: (id: string) => ["task", id] as const,
  approvals: ["approvals"] as const,
  nodes: ["nodes"] as const,
  node: (id: string) => ["node", id] as const,
  files: (node: string, path: string) => ["files", node, path] as const,
  devices: ["devices"] as const,
  search: (q: string) => ["vault-search", q] as const,
  note: (key: string) => ["note", key] as const,
  graph: ["graph"] as const,
  facts: ["facts"] as const,
};

export function useAgents() {
  return useQuery({
    queryKey: qk.agents,
    queryFn: async () => (await api().agents.listAgents({ includeDisabled: true })).agents,
  });
}

export function useAgent(id: string | undefined) {
  return useQuery({
    queryKey: qk.agent(id ?? ""),
    enabled: !!id,
    queryFn: async () => api().agents.getAgent({ id: id! }),
  });
}

/** Seeds the event store with the task list so pages render before the stream replays. */
export function useTaskList(filter: { states?: TaskState[]; agentId?: string; nodeId?: string; rootsOnly?: boolean } = {}) {
  const upsert = useEvents((s) => s.upsertTasks);
  const q = useQuery({
    queryKey: qk.tasks(filter),
    queryFn: async () =>
      (
        await api().tasks.listTasks({
          page: { pageSize: 200 },
          states: filter.states ?? [],
          agentId: filter.agentId ?? "",
          nodeId: filter.nodeId ?? "",
          rootsOnly: filter.rootsOnly ?? false,
        })
      ).tasks,
  });
  useEffect(() => {
    if (q.data) upsert(q.data);
  }, [q.data, upsert]);
  return q;
}

export function useTaskQuery(id: string | undefined) {
  const upsert = useEvents((s) => s.upsertTasks);
  const q = useQuery({
    queryKey: qk.task(id ?? ""),
    enabled: !!id,
    queryFn: async () => api().tasks.getTask({ id: id!, includeChildren: true }),
  });
  useEffect(() => {
    if (q.data) upsert([...(q.data.task ? [q.data.task] : []), ...q.data.children]);
  }, [q.data, upsert]);
  return q;
}

export function usePendingApprovalsSeed() {
  const upsert = useEvents((s) => s.upsertApprovals);
  const q = useQuery({
    queryKey: qk.approvals,
    queryFn: async () => (await api().approvals.listApprovals({ states: [] })).approvals,
  });
  useEffect(() => {
    if (q.data) upsert(q.data);
  }, [q.data, upsert]);
  return q;
}

export function useNodes() {
  const upsert = useEvents((s) => s.upsertNodeStatus);
  const q = useQuery({
    queryKey: qk.nodes,
    queryFn: async () => (await api().nodes.listNodes({})).nodes,
  });
  useEffect(() => {
    if (q.data) upsert(q.data.flatMap((n) => (n.status ? [n.status] : [])));
  }, [q.data, upsert]);
  return q;
}

export function useDevices() {
  return useQuery({
    queryKey: qk.devices,
    queryFn: async () => (await api().devices.listDevices({})).devices,
  });
}

// ---- mutations (optimistic, with rollback) ------------------------------------------------------

/**
 * Approve / deny. The card leaves the inbox immediately; if the Hub refuses, the approval is
 * restored and an error toast offers a retry.
 */
export function useDecide() {
  const deviceId = useSettings((s) => s.deviceId);
  const m = useMutation({
    mutationFn: async ({ approval, approve, comment }: { approval: Approval; approve: boolean; comment?: string }) =>
      api().approvals.decide({
        decision: {
          approvalId: approval.id,
          approve,
          challenge: approval.challenge,
          // Bind the decision to exactly the action shown (the Hub rejects mismatches).
          actionSha256: approval.payloadHash,
          expiresAt: approval.expiresAt,
          deviceId,
          // The web client cannot sign with a Secure Enclave key; the Hub only accepts unsigned
          // decisions for `approve`-level approvals from authenticated sessions.
        },
        comment: comment ?? "",
      }),
    onMutate: ({ approval, approve }) => {
      const prev = useEvents.getState().approvals[approval.id] ?? approval;
      useEvents.getState().putApproval({ ...prev, state: approve ? ApprovalState.APPROVED : ApprovalState.DENIED });
      return { prev };
    },
    onSuccess: (res, vars) => {
      if (res.approval) useEvents.getState().putApproval(res.approval);
      toast.success(vars.approve ? "Approved" : "Denied", vars.approval.title);
    },
    onError: (err, vars, ctx) => {
      if (ctx?.prev) useEvents.getState().putApproval(ctx.prev);
      toast.error(vars.approve ? "Approval failed" : "Deny failed", err, { label: "Retry", onClick: () => m.mutate(vars) });
    },
  });
  return m;
}

export interface PendingMessage {
  id: string;
  taskId: string;
  text: string;
  status: "sending" | "failed";
}

/** Replies sent from the run view that the stream hasn't echoed back yet. */
export const usePendingMessages = create<{ items: PendingMessage[]; set: (f: (l: PendingMessage[]) => PendingMessage[]) => void }>((set) => ({
  items: [],
  set: (f) => set((s) => ({ items: f(s.items) })),
}));

export function useSendMessage() {
  const m = useMutation({
    mutationFn: async (v: { taskId: string; text?: string; questionId?: string; choice?: string; localId?: string }) =>
      api().tasks.sendMessage({
        taskId: v.taskId,
        text: v.text ?? "",
        questionId: v.questionId ?? "",
        choice: v.choice ?? "",
        idempotencyKey: v.localId ?? crypto.randomUUID(),
      }),
    onMutate: (v) => {
      if (v.questionId) useEvents.getState().setAnswered(v.questionId, true);
      if (v.text && !v.questionId) {
        const id = v.localId ?? crypto.randomUUID();
        v.localId = id;
        usePendingMessages.getState().set((l) => [...l.filter((x) => x.id !== id), { id, taskId: v.taskId, text: v.text!, status: "sending" }]);
      }
    },
    onSuccess: (_r, v) => {
      // The userMessage event normally arrives within a frame; drop the placeholder shortly after.
      if (v.localId) setTimeout(() => usePendingMessages.getState().set((l) => l.filter((x) => x.id !== v.localId)), 1500);
    },
    onError: (err, v) => {
      if (v.questionId) useEvents.getState().setAnswered(v.questionId, false);
      if (v.localId) usePendingMessages.getState().set((l) => l.map((x) => (x.id === v.localId ? { ...x, status: "failed" } : x)));
      toast.error(v.questionId ? "Answer not sent" : "Message not sent", err, { label: "Retry", onClick: () => m.mutate(v) });
    },
  });
  return m;
}

export function useCancelTask() {
  const m = useMutation({
    mutationFn: async (id: string) => api().tasks.cancelTask({ id }),
    onMutate: (id) => {
      const prev = useEvents.getState().tasks[id];
      // Keep updatedAt so the Hub's next TaskUpdated always wins over the optimistic state.
      if (prev) useEvents.getState().putTask({ ...prev, state: TaskState.CANCELLED });
      return { prev };
    },
    onSuccess: () => toast.info("Cancelled"),
    onError: (err, id, ctx) => {
      if (ctx?.prev) useEvents.getState().putTask(ctx.prev);
      toast.error("Cancel failed", err, { label: "Retry", onClick: () => m.mutate(id) });
    },
  });
  return m;
}

export function useEscalateTask() {
  const upsert = useEvents((s) => s.upsertTasks);
  return useMutation({
    mutationFn: async (v: { id: string; hint?: string }) => api().tasks.escalateTask({ id: v.id, hint: v.hint ?? "" }),
    onSuccess: (res) => {
      if (res.task) upsert([res.task]);
      toast.success("Escalated", "Retrying on the next tier");
    },
    onError: (err) => toast.error("Escalation failed", err),
  });
}

export function useRetryTask() {
  const upsert = useEvents((s) => s.upsertTasks);
  return useMutation({
    mutationFn: async (v: { prompt: string; agentId: string }) =>
      api().tasks.createTask({ prompt: v.prompt, agentId: v.agentId, source: "web", idempotencyKey: crypto.randomUUID() }),
    onSuccess: (res) => {
      if (res.task) upsert([res.task]);
      toast.success("Retry started");
    },
    onError: (err) => toast.error("Retry failed", err),
  });
}

export function useInvalidate() {
  const qc = useQueryClient();
  return (key: readonly unknown[]) => qc.invalidateQueries({ queryKey: key });
}
