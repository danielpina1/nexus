import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, CheckCircle2, RotateCcw, Save } from "lucide-react";
import { ConnectError } from "@connectrpc/connect";
import { api } from "../lib/api";
import { qk, useAgent } from "../lib/queries";
import { parseAgentMarkdown } from "../lib/agentParse";
import { splitFrontmatter } from "../lib/yaml";
import { toast } from "../lib/toast";
import { useAllTasks } from "../lib/selectors";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Badge } from "../components/ui/Badge";
import { Input, Textarea } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { Skeleton } from "../components/ui/Skeleton";
import { ErrorState } from "../components/ui/ErrorState";
import { Markdown } from "../components/Markdown";
import { AgentIcon } from "../components/AgentIcon";
import { TaskRow } from "../components/TaskRow";
import { formatDuration, formatMoney, tierLabel, trustLabel } from "../lib/format";
import type { Agent } from "../gen/nexus/v1/agent_pb";

const TEMPLATE = `---
type: agent
id: my-agent
name: My agent
description: What this agent does and when the router should pick it.
icon: sparkles
runtime: openai
model: smart
tier: medium
node: { tags: [any] }
tools: [memory.search, vault.read, ask_user]
limits: { max_runtime: 10m, max_cost_eur: 0.20 }
version: 1
---

# My agent

Instructions for the agent.

## Lessons learned
`;

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-3 py-1.5 text-[13px]">
      <span className="text-muted">{label}</span>
      <span className="min-w-0 text-fg">{children}</span>
    </div>
  );
}

function ParsedFields({ agent }: { agent: Agent }) {
  const l = agent.limits;
  return (
    <div className="divide-y divide-border/60">
      <Row label="Name">{agent.name || "—"}</Row>
      <Row label="ID">
        <span className="font-mono">{agent.id || "—"}</span>
      </Row>
      <Row label="Description">{agent.description || "—"}</Row>
      <Row label="Runtime">
        <Badge tone="accent">{agent.runtime || "—"}</Badge> {agent.model && <Badge>{agent.model}</Badge>}
      </Row>
      <Row label="Tier">{tierLabel[agent.tier]}</Row>
      <Row label="Node tags">
        <div className="flex flex-wrap gap-1">{agent.nodeTags.length ? agent.nodeTags.map((t) => <Badge key={t}>#{t}</Badge>) : "any"}</div>
      </Row>
      <Row label="Tools">
        <div className="flex flex-wrap gap-1">
          {agent.tools.map((t) => (
            <Badge key={t} className="font-mono">
              {t}
            </Badge>
          ))}
        </div>
      </Row>
      <Row label="Limits">
        <div className="flex flex-wrap gap-x-3 gap-y-1 text-fg-2">
          {l?.maxRuntimeSeconds ? <span>⏱ {formatDuration(Number(l.maxRuntimeSeconds) * 1000)}</span> : null}
          {l?.maxCost ? <span>model ≤ {formatMoney(Number(l.maxCost.minorUnits), l.maxCost.currency)}</span> : null}
          {l?.maxSpend ? <span>spend ≤ {formatMoney(Number(l.maxSpend.minorUnits), l.maxSpend.currency)}</span> : null}
          {!l?.maxRuntimeSeconds && !l?.maxCost && !l?.maxSpend && "—"}
        </div>
      </Row>
      <Row label="Policy">
        <div className="flex flex-wrap gap-1">
          {Object.entries(agent.policy).length
            ? Object.entries(agent.policy).map(([k, v]) => (
                <Badge key={k} tone={v >= 4 ? "danger" : v >= 3 ? "warn" : "neutral"}>
                  {k}: {trustLabel[v]}
                </Badge>
              ))
            : "defaults"}
        </div>
      </Row>
      <Row label="Version">v{agent.version}</Row>
    </div>
  );
}

function errorLines(err: unknown): string[] {
  if (err instanceof ConnectError) return (err.rawMessage || err.message).split("\n").filter(Boolean);
  if (err instanceof Error) return [err.message];
  return [String(err)];
}

export default function AgentDetail() {
  const { id = "" } = useParams();
  const isNew = id === "new";
  const q = useAgent(isNew ? undefined : id);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [md, setMd] = useState<string | null>(isNew ? TEMPLATE : null);
  const [commitMsg, setCommitMsg] = useState("");
  const [tab, setTab] = useState<"edit" | "preview" | "runs">("edit");
  const tasks = useAllTasks();

  // md === null means "unedited": show what the Hub has.
  const text = md ?? q.data?.markdown ?? "";
  const local = useMemo(() => parseAgentMarkdown(text), [text]);
  const dirty = isNew || (md !== null && md !== q.data?.markdown);
  const body = useMemo(() => splitFrontmatter(text).body, [text]);

  const save = useMutation({
    mutationFn: async () => api().agents.upsertAgent({ markdown: text, commitMessage: commitMsg || `${isNew ? "Create" : "Update"} agent ${local.id}` }),
    onSuccess: async (res) => {
      const a = res.agent;
      if (a?.errors.length) {
        toast.warn("Saved with validation errors");
      } else {
        toast.success(isNew ? "Agent created" : "Agent saved", a ? `${a.name} v${a.version}` : undefined);
      }
      setCommitMsg("");
      await qc.invalidateQueries({ queryKey: qk.agents });
      if (a) {
        qc.removeQueries({ queryKey: qk.agent(a.id) });
        setMd(null);
        if (isNew || a.id !== id) navigate(`/agents/${a.id}`, { replace: true });
      }
    },
  });

  if (!isNew && q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  if (!isNew && !q.data) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-72" />
        <Skeleton className="h-[480px]" />
      </div>
    );
  }

  const server = q.data?.agent;
  const serverErrors = save.isError ? errorLines(save.error) : (save.data?.agent?.errors ?? server?.errors ?? []);
  const runs = tasks.filter((t) => t.agentId === id).slice(0, 25);

  return (
    <div>
      <Link to="/agents" className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ArrowLeft className="size-3.5" /> Agents
      </Link>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <AgentIcon agentId={local.id || id} icon={local.icon} size="lg" />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-xl font-semibold tracking-tight text-fg">{local.name || (isNew ? "New agent" : id)}</h1>
          <div className="font-mono text-xs text-muted">{server?.note?.path || `agents/${local.id || "…"}.md`}</div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {dirty && !isNew && (
            <Button variant="ghost" icon={<RotateCcw className="size-3.5" />} onClick={() => setMd(null)}>
              Revert
            </Button>
          )}
          <Input value={commitMsg} onChange={(e) => setCommitMsg(e.target.value)} placeholder="Commit message (optional)" className="w-56" />
          <Button variant="primary" icon={<Save className="size-3.5" />} loading={save.isPending} disabled={!dirty} onClick={() => save.mutate()}>
            {isNew ? "Create" : "Save"}
          </Button>
        </div>
      </div>

      {(serverErrors.length > 0 || local.errors.length > 0) && (
        <Card className="mb-5 border-danger/40 bg-danger-soft/50 p-4">
          <div className="mb-1.5 flex items-center gap-2 text-[13px] font-semibold text-danger">
            <AlertTriangle className="size-4" />
            {save.isError ? "The Hub rejected this agent" : "Validation"}
          </div>
          <ul className="list-disc space-y-0.5 pl-5 text-[13px] text-fg-2">
            {(save.isError ? serverErrors : [...new Set([...local.errors, ...serverErrors])]).map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        </Card>
      )}

      <Tabs<"edit" | "preview" | "runs">
        className="mb-4"
        value={tab}
        onChange={setTab}
        items={[
          { id: "edit", label: "Edit" },
          { id: "preview", label: "Instructions" },
          { id: "runs", label: "Runs", count: runs.length },
        ]}
      />

      {tab === "edit" && (
        <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_380px]">
          <Card className="overflow-hidden">
            <CardHeader title="Markdown" subtitle="Frontmatter + instructions. Saved to the vault as a git commit." actions={dirty ? <Badge tone="warn">unsaved</Badge> : <Badge tone="ok"><CheckCircle2 className="size-3" />saved</Badge>} />
            <Textarea
              value={text}
              onChange={(e) => setMd(e.target.value)}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === "s") {
                  e.preventDefault();
                  if (dirty) save.mutate();
                }
                if (e.key === "Tab") {
                  e.preventDefault();
                  const el = e.currentTarget;
                  const s = el.selectionStart;
                  setMd(text.slice(0, s) + "  " + text.slice(el.selectionEnd));
                  requestAnimationFrame(() => el.setSelectionRange(s + 2, s + 2));
                }
              }}
              spellCheck={false}
              className="min-h-[520px] resize-y rounded-none border-0 bg-surface font-mono text-[12.5px] leading-relaxed focus:ring-0"
            />
          </Card>
          <Card>
            <CardHeader title="Parsed" subtitle="Live preview of what the Hub will load" />
            <div className="px-4 py-2">
              <ParsedFields agent={local} />
            </div>
          </Card>
        </div>
      )}
      {tab === "preview" && (
        <Card className="p-6">
          <Markdown>{body}</Markdown>
        </Card>
      )}
      {tab === "runs" && (
        <Card className="overflow-hidden">
          {runs.length === 0 ? (
            <div className="p-8 text-center text-[13px] text-muted">No runs yet.</div>
          ) : (
            <div className="divide-y divide-border">
              {runs.map((t) => (
                <TaskRow key={t.id} task={t} />
              ))}
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
