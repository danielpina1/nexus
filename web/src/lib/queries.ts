import { QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { api } from "./api";
import { useEvents } from "./store";
import { toast } from "./toast";
import type { Approval } from "../gen/nexus/v1/approval_pb";
import type { TaskState } from "../gen/nexus/v1/task_pb";
import { useSettings } from "./settings";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

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
    refetchInterval: 60_000,
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

// ---- mutations -----------------------------------------------------------------------------------

export function useDecide() {
  const upsert = useEvents((s) => s.upsertApprovals);
  const deviceId = useSettings((s) => s.deviceId);
  return useMutation({
    mutationFn: async ({ approval, approve, comment }: { approval: Approval; approve: boolean; comment?: string }) =>
      api().approvals.decide({
        decision: {
          approvalId: approval.id,
          approve,
          challenge: approval.challenge,
          expiresAt: approval.expiresAt,
          deviceId,
          // The web client cannot sign with a Secure Enclave key; the Hub only accepts unsigned
          // decisions for `approve`-level approvals from authenticated sessions.
        },
        comment: comment ?? "",
      }),
    onSuccess: (res, vars) => {
      if (res.approval) upsert([res.approval]);
      if (vars.approve) toast.success("Approved", vars.approval.title);
      else toast.info("Denied", vars.approval.title);
    },
    onError: (err) => toast.error("Decision failed", err),
  });
}

export function useSendMessage() {
  const markAnswered = useEvents((s) => s.markAnswered);
  return useMutation({
    mutationFn: async (v: { taskId: string; text?: string; questionId?: string; choice?: string }) =>
      api().tasks.sendMessage({
        taskId: v.taskId,
        text: v.text ?? "",
        questionId: v.questionId ?? "",
        choice: v.choice ?? "",
        idempotencyKey: crypto.randomUUID(),
      }),
    onSuccess: (_r, v) => {
      if (v.questionId) markAnswered(v.questionId);
    },
    onError: (err) => toast.error("Message not sent", err),
  });
}

export function useCancelTask() {
  return useMutation({
    mutationFn: async (id: string) => api().tasks.cancelTask({ id }),
    onSuccess: () => toast.info("Cancel requested"),
    onError: (err) => toast.error("Cancel failed", err),
  });
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
