import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, Bot, ChevronRight, FlaskConical, Loader2, PenTool, ShieldCheck, Sparkles, Users, Wand2, Workflow } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { Label, Textarea } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { SkeletonRows } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { EmptyState } from "../components/ui/EmptyState";
import { VirtualList } from "../components/VirtualList";
import { useAgents } from "../lib/queries";
import { BUSY_STATES, OPEN_STATES, draftStateLabel, draftStateTone, draftTitle, useCreateDraft, useDrafts } from "../lib/architect";
import { cx, timeAgo, tsMs } from "../lib/format";
import { useNow } from "../lib/useNow";
import { prefetchPath } from "../app/registry";
import { DraftKind, DraftState, type AgentDraft } from "../gen/nexus/v1/architect_pb";

const KINDS: Array<{ id: DraftKind; label: string; hint: string; icon: React.ReactNode }> = [
  { id: DraftKind.AUTO, label: "Let it decide", hint: "Agent, plus a workflow or team if needed", icon: <Sparkles /> },
  { id: DraftKind.AGENT, label: "Agent", hint: "One agent note", icon: <Bot /> },
  { id: DraftKind.TEAM, label: "Team", hint: "An orchestrator and members", icon: <Users /> },
  { id: DraftKind.WORKFLOW, label: "Workflow", hint: "A scheduled or triggered workflow", icon: <Workflow /> },
];

const EXAMPLES = [
  "Tell me when the AirPods Pro drop below €150 at Worten or Fnac",
  "Every Friday, summarise what changed in my nexus repo this week",
  "A team that plans a weekend trip: one researches places, one checks the weather, one books",
  "Watch my electricity bill emails and alert me if a month is 20 % above average",
];

function kindIcon(k: DraftKind) {
  return k === DraftKind.TEAM ? <Users /> : k === DraftKind.WORKFLOW ? <Workflow /> : <Bot />;
}

function StartCard() {
  const [params] = useSearchParams();
  const initialKind = { team: DraftKind.TEAM, agent: DraftKind.AGENT, workflow: DraftKind.WORKFLOW }[params.get("kind") ?? ""] ?? DraftKind.AUTO;
  const [mode, setMode] = useState<"new" | "improve">(params.get("improve") ? "improve" : "new");
  const [kind, setKind] = useState<DraftKind>(initialKind);
  const [agentId, setAgentId] = useState(params.get("improve") ?? "");
  const [request, setRequest] = useState(params.get("q") ?? "");
  const [context, setContext] = useState("");
  const { data: agents } = useAgents();
  const create = useCreateDraft();
  const navigate = useNavigate();
  const improving = mode === "improve";
  const missingAgent = improving && !agentId;
  const canSubmit = request.trim().length > 3 && !missingAgent && !create.isPending;

  const submit = () => {
    if (!canSubmit) return;
    create.mutate(
      { request: request.trim(), kind: improving ? DraftKind.AGENT : kind, context: context.trim(), updateAgentId: improving ? agentId : "" },
      { onSuccess: (d) => d && navigate(`/architect/${d.id}`) },
    );
  };

  return (
    <Card className="p-4 sm:p-5">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
        className="space-y-4"
      >
        <Tabs<"new" | "improve">
          label="What to do"
          value={mode}
          onChange={setMode}
          items={[
            { id: "new", label: "Create new" },
            { id: "improve", label: "Improve an agent" },
          ]}
        />

        {improving && (
          <div>
            <Label hint="The Architect drafts a new version with a diff against the current note.">Agent to improve</Label>
            <select
              aria-label="Agent to improve"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              className="h-9 w-full rounded-lg border border-border-input bg-surface px-2.5 text-base text-fg sm:text-[13px] pointer-coarse:h-11"
              data-testid="architect-agent"
            >
              <option value="">Choose an agent…</option>
              {(agents ?? []).map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.id})
                </option>
              ))}
            </select>
          </div>
        )}

        <div>
          <label htmlFor="architect-request" className="mb-1.5 block text-[13px] font-medium text-fg">
            {improving ? "What should change?" : "Describe the agent you want"}
          </label>
          <Textarea
            id="architect-request"
            data-testid="architect-request"
            rows={4}
            value={request}
            onChange={(e) => setRequest(e.target.value)}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                e.preventDefault();
                submit();
              }
            }}
            placeholder={improving ? "e.g. It keeps substituting lactose milk; never do that, ask instead." : "Describe the agent you want…  e.g. “Tell me when the AirPods drop below €150”"}
            className="resize-y text-base sm:text-[14px]"
            autoFocus
          />
          {!improving && !request && (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {EXAMPLES.map((x) => (
                <button
                  key={x}
                  type="button"
                  onClick={() => setRequest(x)}
                  className="min-h-8 max-w-full truncate rounded-full border border-border bg-surface px-3 text-left text-xs text-fg-2 transition-colors hover:border-border-strong hover:text-fg pointer-coarse:min-h-11"
                >
                  {x}
                </button>
              ))}
            </div>
          )}
        </div>

        {!improving && (
          <fieldset>
            <legend className="mb-1.5 text-[13px] font-medium text-fg">What should it make?</legend>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {KINDS.map((k) => {
                const on = kind === k.id;
                return (
                  <label
                    key={k.id}
                    className={cx(
                      "flex min-h-[64px] cursor-pointer flex-col gap-0.5 rounded-lg border p-2.5 text-[13px] transition-colors duration-150 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-ring",
                      on ? "border-accent bg-accent-soft" : "border-border-input bg-surface hover:bg-surface-2",
                    )}
                  >
                    <input type="radio" name="kind" className="sr-only" checked={on} onChange={() => setKind(k.id)} />
                    <span className={cx("flex items-center gap-1.5 font-medium [&_svg]:size-3.5", on ? "text-accent" : "text-fg")}>
                      {k.icon}
                      {k.label}
                    </span>
                    <span className="text-[11.5px] leading-snug text-muted">{k.hint}</span>
                  </label>
                );
              })}
            </div>
          </fieldset>
        )}

        <details className="group">
          <summary className="flex min-h-8 cursor-pointer list-none items-center gap-1 text-[13px] text-muted hover:text-fg">
            <ChevronRight className="size-3.5 transition-transform group-open:rotate-90" aria-hidden /> Add context (optional)
          </summary>
          <Textarea
            rows={3}
            value={context}
            onChange={(e) => setContext(e.target.value)}
            placeholder="Anything the Architect should know: accounts, stores, constraints, how you'd like to be notified…"
            aria-label="Context"
            className="mt-2 resize-y"
          />
        </details>

        <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
          <ol className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted" aria-label="How it works">
            {[
              [<PenTool key="i" />, "Draft"],
              [<ShieldCheck key="i" />, "Validate & auto-fix"],
              [<FlaskConical key="i" />, "Test in a sandbox"],
              [<Wand2 key="i" />, "Publish after you approve"],
            ].map(([icon, label], i) => (
              <li key={i} className="flex items-center gap-1.5">
                {i > 0 && <ChevronRight className="size-3" aria-hidden />}
                <span className="[&_svg]:size-3.5" aria-hidden>
                  {icon}
                </span>
                {label}
              </li>
            ))}
          </ol>
          <Button type="submit" variant="primary" size="md" icon={<Sparkles className="size-4" />} loading={create.isPending} disabled={!canSubmit} data-testid="architect-submit">
            {improving ? "Draft an improvement" : "Draft it"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function DraftRow({ d, now }: { d: AgentDraft; now: number }) {
  const busy = BUSY_STATES.has(d.state);
  return (
    <Link
      to={`/architect/${d.id}`}
      onMouseEnter={() => prefetchPath(`/architect/${d.id}`)}
      onFocus={() => prefetchPath(`/architect/${d.id}`)}
      data-testid="draft-row"
      className="flex min-h-[60px] items-center gap-3 px-4 py-2.5 transition-colors duration-150 hover:bg-surface-2"
    >
      <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-2 text-muted [&_svg]:size-4" aria-hidden>
        {kindIcon(d.kind)}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cx("truncate text-[13px] font-medium text-fg", d.notes.length > 0 && "font-mono")}>{draftTitle(d)}</span>
          {d.updateAgentId && <Badge>update</Badge>}
          {d.problems.length > 0 && (
            <Badge tone="danger">
              <AlertTriangle className="size-3" aria-hidden />
              {d.problems.length}
            </Badge>
          )}
        </div>
        <div className="mt-0.5 truncate text-xs text-muted">{d.request}</div>
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1">
        <Badge tone={draftStateTone[d.state]}>
          {busy && <Loader2 className="size-3 animate-spin" aria-hidden />}
          {draftStateLabel[d.state]}
        </Badge>
        <span className="text-[11px] text-muted tabular">
          rev {d.revision} · {timeAgo(tsMs(d.updatedAt), now)}
        </span>
      </div>
    </Link>
  );
}

type Filter = "open" | "published" | "all";

function DraftList() {
  const q = useDrafts();
  const [filter, setFilter] = useState<Filter>("open");
  const now = useNow(30_000);
  const drafts = useMemo(() => (q.data ?? []).slice().sort((a, b) => tsMs(b.updatedAt) - tsMs(a.updatedAt)), [q.data]);
  const shown = drafts.filter((d) => (filter === "all" ? true : filter === "open" ? OPEN_STATES.has(d.state) : d.state === DraftState.PUBLISHED));
  const openCount = drafts.filter((d) => OPEN_STATES.has(d.state)).length;
  return (
    <Card className="min-w-0 overflow-hidden">
      <CardHeader
        title="Drafts"
        subtitle="Kept until you publish or discard them"
        actions={
          <Tabs<Filter>
            label="Filter drafts"
            value={filter}
            onChange={setFilter}
            items={[
              { id: "open", label: "Open", count: openCount },
              { id: "published", label: "Published" },
              { id: "all", label: "All" },
            ]}
          />
        }
        className="flex-wrap"
      />
      {q.isLoading ? (
        <SkeletonRows rows={4} />
      ) : q.isError ? (
        <ErrorState compact error={q.error} onRetry={() => q.refetch()} />
      ) : shown.length === 0 ? (
        <EmptyState
          compact
          icon={<PenTool />}
          title={filter === "open" ? "No open drafts" : filter === "published" ? "Nothing published yet" : "No drafts yet"}
          body={filter === "open" ? "Describe an agent on the left to start one." : undefined}
        />
      ) : (
        <VirtualList className="divide-y divide-border" items={shown} getKey={(d) => d.id} estimate={61} render={(d) => <DraftRow d={d} now={now} />} />
      )}
    </Card>
  );
}

export default function Architect() {
  return (
    <div>
      <PageHeader title="Agent Architect" subtitle="Describe what you need in plain language. The Architect designs the agent, validates and tests it in a sandbox, and publishes only after you approve." />
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] xl:items-start">
        <StartCard />
        <DraftList />
      </div>
    </div>
  );
}

