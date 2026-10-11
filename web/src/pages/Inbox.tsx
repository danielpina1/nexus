import { useMemo, useState } from "react";
import { Check, CheckCheck, Inbox as InboxIcon, ListChecks, X } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Tabs } from "../components/ui/Tabs";
import { Button } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { ApprovalCard, NoticeCard, QuestionCard } from "../components/InboxCards";
import { useInbox, type InboxItem } from "../lib/selectors";
import { useEvents } from "../lib/store";
import { useDecide } from "../lib/queries";
import { TrustLevel } from "../gen/nexus/v1/common_pb";
import { ApprovalState } from "../gen/nexus/v1/approval_pb";
import { tsMs } from "../lib/format";

type Filter = "all" | "approval" | "question" | "notice" | "history";

export default function Inbox() {
  const items = useInbox();
  const approvals = useEvents((s) => s.approvals);
  const dismiss = useEvents((s) => s.dismiss);
  const decide = useDecide();
  const [filter, setFilter] = useState<Filter>("all");
  const [bulk, setBulk] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const visible = useMemo(() => (filter === "all" ? items : items.filter((i) => i.kind === filter)), [items, filter]);
  const history = useMemo(
    () => Object.values(approvals).filter((a) => a.state !== ApprovalState.PENDING).sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt)).slice(0, 30),
    [approvals],
  );
  const count = (k: InboxItem["kind"]) => items.filter((i) => i.kind === k).length;

  const sel = (id: string) => (v: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (v) n.add(id);
      else n.delete(id);
      return n;
    });
  const chosen = items.filter((i) => selected.has(i.id));
  const chosenApprovals = chosen.flatMap((i) => (i.kind === "approval" ? [i.approval] : []));
  const approvable = chosenApprovals.filter((a) => a.level !== TrustLevel.BIOMETRIC);
  const chosenNotices = chosen.filter((i) => i.kind !== "approval").map((i) => i.id);

  const runBulk = async (approve: boolean) => {
    const list = approve ? approvable : chosenApprovals;
    for (const a of list) await decide.mutateAsync({ approval: a, approve }).catch(() => undefined);
    setSelected(new Set());
  };

  return (
    <div>
      <PageHeader
        icon={<InboxIcon />}
        title="Inbox"
        subtitle="Approvals, questions and results that need you — ranked by urgency."
        actions={
          <Button variant={bulk ? "primary" : "secondary"} icon={<ListChecks className="size-3.5" />} onClick={() => { setBulk((b) => !b); setSelected(new Set()); }}>
            {bulk ? "Done" : "Select"}
          </Button>
        }
      />
      <Tabs<Filter>
        className="mb-4"
        value={filter}
        onChange={setFilter}
        items={[
          { id: "all", label: "All", count: items.length },
          { id: "approval", label: "Approvals", count: count("approval") },
          { id: "question", label: "Questions", count: count("question") },
          { id: "notice", label: "Notices", count: count("notice") },
          { id: "history", label: "Decided" },
        ]}
      />

      {bulk && (
        <div className="sticky top-0 z-10 mb-4 flex flex-wrap items-center gap-2 rounded-xl border border-border bg-surface/95 p-2.5 shadow-card backdrop-blur">
          <span className="px-1 text-[13px] text-fg-2">{selected.size} selected</span>
          <Button size="xs" variant="ghost" onClick={() => setSelected(new Set(visible.map((v) => v.id)))}>
            Select all
          </Button>
          <span className="flex-1" />
          <Button size="sm" variant="success" icon={<Check className="size-3.5" />} disabled={approvable.length === 0} loading={decide.isPending} onClick={() => runBulk(true)}>
            Approve {approvable.length || ""}
          </Button>
          <Button size="sm" icon={<X className="size-3.5" />} disabled={chosenApprovals.length === 0} onClick={() => runBulk(false)}>
            Deny {chosenApprovals.length || ""}
          </Button>
          <Button
            size="sm"
            icon={<CheckCheck className="size-3.5" />}
            disabled={chosenNotices.length === 0}
            onClick={() => {
              dismiss(chosenNotices);
              setSelected(new Set());
            }}
          >
            Dismiss {chosenNotices.length || ""}
          </Button>
          {chosenApprovals.length > approvable.length && <span className="w-full px-1 text-xs text-muted">Face ID approvals can only be approved on your iPhone.</span>}
        </div>
      )}

      {filter === "history" ? (
        <div className="space-y-3">
          {history.length === 0 && <EmptyState icon={<CheckCheck />} title="No decisions yet" />}
          {history.map((a) => (
            <ApprovalCard key={a.id} approval={a} />
          ))}
        </div>
      ) : visible.length === 0 ? (
        <EmptyState icon={<CheckCheck />} title="All clear" body="Nothing needs you right now. New approvals and questions appear here instantly." />
      ) : (
        <div className="space-y-3">
          {visible.map((it) =>
            it.kind === "approval" ? (
              <ApprovalCard key={it.id} approval={it.approval} selectable={bulk} selected={selected.has(it.id)} onSelect={sel(it.id)} />
            ) : it.kind === "question" ? (
              <QuestionCard key={it.id} item={it.item} />
            ) : (
              <NoticeCard key={it.id} item={it.item} selectable={bulk} selected={selected.has(it.id)} onSelect={sel(it.id)} />
            ),
          )}
        </div>
      )}
    </div>
  );
}
