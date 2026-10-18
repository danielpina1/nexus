import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import { useEvents, type NoticeItem } from "./store";
import { toast } from "./toast";
import type { Tone } from "./format";
import { DraftKind, DraftState, type AgentDraft } from "../gen/nexus/v1/architect_pb";

export const draftKeys = {
  drafts: ["drafts"] as const,
  draft: (id: string) => ["draft", id] as const,
};

export const draftStateLabel: Record<DraftState, string> = {
  [DraftState.UNSPECIFIED]: "Unknown",
  [DraftState.DRAFTING]: "Drafting",
  [DraftState.READY]: "Ready",
  [DraftState.INVALID]: "Has problems",
  [DraftState.TESTING]: "Testing",
  [DraftState.PENDING_APPROVAL]: "Awaiting approval",
  [DraftState.PUBLISHED]: "Published",
  [DraftState.DISCARDED]: "Discarded",
  [DraftState.FAILED]: "Failed",
};

export const draftStateTone: Record<DraftState, Tone> = {
  [DraftState.UNSPECIFIED]: "neutral",
  [DraftState.DRAFTING]: "info",
  [DraftState.READY]: "accent",
  [DraftState.INVALID]: "danger",
  [DraftState.TESTING]: "info",
  [DraftState.PENDING_APPROVAL]: "warn",
  [DraftState.PUBLISHED]: "ok",
  [DraftState.DISCARDED]: "neutral",
  [DraftState.FAILED]: "danger",
};

export const draftKindLabel: Record<DraftKind, string> = {
  [DraftKind.UNSPECIFIED]: "auto",
  [DraftKind.AUTO]: "auto",
  [DraftKind.AGENT]: "agent",
  [DraftKind.TEAM]: "team",
  [DraftKind.WORKFLOW]: "workflow",
};

export const BUSY_STATES = new Set([DraftState.DRAFTING, DraftState.TESTING]);
export const OPEN_STATES = new Set([DraftState.DRAFTING, DraftState.READY, DraftState.INVALID, DraftState.TESTING, DraftState.PENDING_APPROVAL, DraftState.FAILED]);

/** A short name for a draft: its first note id, else the request. */
export function draftTitle(d: AgentDraft): string {
  const n = d.notes.find((x) => x.kind === "agent") ?? d.notes[0];
  if (n?.id) return n.id;
  if (d.updateAgentId) return `Improve ${d.updateAgentId}`;
  return d.request.length > 70 ? d.request.slice(0, 68) + "…" : d.request || d.id;
}

export function useDrafts() {
  return useQuery({
    queryKey: draftKeys.drafts,
    queryFn: async () => (await api().architect.listDrafts({ limit: 100 })).drafts,
  });
}

export function useDraft(id: string | undefined) {
  return useQuery({
    queryKey: draftKeys.draft(id ?? ""),
    enabled: !!id,
    queryFn: async () => (await api().architect.getDraft({ id: id! })).draft,
  });
}

/** Inbox notices about a draft (data.draft_id), oldest first. Live from the shared stream. */
export function useDraftNotices(draftId: string | undefined): NoticeItem[] {
  const notices = useEvents((s) => s.notices);
  return useMemo(
    () => (draftId ? Object.values(notices).filter((n) => n.notice.data?.draft_id === draftId).sort((a, b) => a.at - b.at) : []),
    [notices, draftId],
  );
}

function useDraftMutation<V>(fn: (v: V) => Promise<AgentDraft | undefined>, opts: { ok?: string; fail: string }) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (d) => {
      if (d) qc.setQueryData(draftKeys.draft(d.id), d);
      void qc.invalidateQueries({ queryKey: draftKeys.drafts });
      if (opts.ok) toast.success(opts.ok);
    },
    onError: (err) => toast.error(opts.fail, err),
  });
}

export function useCreateDraft() {
  return useDraftMutation(
    async (v: { request: string; kind: DraftKind; context?: string; updateAgentId?: string }) =>
      (await api().architect.draft({ request: v.request, kind: v.kind, context: v.context ?? "", updateAgentId: v.updateAgentId ?? "", wait: false })).draft,
    { fail: "Couldn't start the draft" },
  );
}

export function useRefineDraft() {
  return useDraftMutation(async (v: { id: string; feedback: string }) => (await api().architect.refine({ draftId: v.id, feedback: v.feedback, wait: false })).draft, {
    ok: "Refining — a new revision is on its way",
    fail: "Refine failed",
  });
}

export function useTestDraft() {
  return useDraftMutation(async (id: string) => (await api().architect.testRun({ draftId: id, wait: false })).draft, { fail: "Test run failed" });
}

export function useDiscardDraft() {
  return useDraftMutation(async (id: string) => (await api().architect.discard({ id })).draft, { ok: "Draft discarded", fail: "Discard failed" });
}

export function usePublishDraft() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => api().architect.publish({ draftId: id }),
    onSuccess: (res) => {
      if (res.draft) qc.setQueryData(draftKeys.draft(res.draft.id), res.draft);
      if (res.approval) useEvents.getState().putApproval(res.approval);
      void qc.invalidateQueries({ queryKey: draftKeys.drafts });
      toast.success("Approval requested", "Approve it here or in the Inbox to publish.");
    },
    onError: (err) => toast.error("Publish failed", err),
  });
}
