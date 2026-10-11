import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, ChevronDown, FileText, Hash, Link2, Network, Search, X } from "lucide-react";
import { api } from "../lib/api";
import { qk } from "../lib/queries";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Input } from "../components/ui/Input";
import { Tabs } from "../components/ui/Tabs";
import { Badge } from "../components/ui/Badge";
import { Skeleton, SkeletonRows } from "../components/ui/Skeleton";
import { EmptyState } from "../components/ui/EmptyState";
import { ErrorState } from "../components/ui/ErrorState";
import { Markdown } from "../components/Markdown";
import { ForceGraph, GraphLegend, groupColor, noteGroup, type GraphNode } from "../components/ForceGraph";
import { cx, formatDateTime, tsDate } from "../lib/format";
import type { NoteRef } from "../gen/nexus/v1/common_pb";

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

function Highlight({ text, query }: { text: string; query: string }) {
  const terms = query.toLowerCase().split(/\s+/).filter((t) => t.length > 1);
  if (!terms.length) return <>{text}</>;
  const re = new RegExp(`(${terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "gi");
  return (
    <>
      {text.split(re).map((part, i) =>
        i % 2 === 1 ? (
          <mark key={i} className="rounded bg-warn-soft px-0.5 text-fg">
            {part}
          </mark>
        ) : (
          part
        ),
      )}
    </>
  );
}

function NoteList({ refs, selected, onSelect }: { refs: NoteRef[]; selected?: string; onSelect: (id: string) => void }) {
  const groups = useMemo(() => {
    const m = new Map<string, NoteRef[]>();
    for (const r of refs) {
      const g = noteGroup(r.path);
      m.set(g, [...(m.get(g) ?? []), r]);
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [refs]);
  const [closed, setClosed] = useState<Set<string>>(new Set());
  return (
    <div className="py-1">
      {groups.map(([g, list]) => (
        <div key={g}>
          <button
            onClick={() => setClosed((s) => { const n = new Set(s); if (n.has(g)) n.delete(g); else n.add(g); return n; })}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-[11px] font-semibold tracking-wide text-muted uppercase hover:text-fg"
          >
            <ChevronDown className={cx("size-3 transition-transform", closed.has(g) && "-rotate-90")} />
            <span className="size-2 rounded-full" style={{ background: groupColor(g) }} />
            {g}
            <span className="font-normal">{list.length}</span>
          </button>
          {!closed.has(g) &&
            list
              .sort((a, b) => a.title.localeCompare(b.title))
              .map((r) => (
                <button
                  key={r.id}
                  onClick={() => onSelect(r.id)}
                  className={cx("flex w-full items-center gap-2 py-1.5 pr-3 pl-8 text-left text-[13px]", selected === r.id ? "bg-accent-soft text-fg" : "text-fg-2 hover:bg-surface-2")}
                >
                  <span className="truncate">{r.title || r.path}</span>
                </button>
              ))}
        </div>
      ))}
    </div>
  );
}

function NoteView({ noteKey, known, onOpen }: { noteKey: string; known: Set<string>; onOpen: (id: string) => void }) {
  const q = useQuery({
    queryKey: qk.note(noteKey),
    queryFn: async () => (await api().vault.getNote({ id: noteKey, path: noteKey })).note,
    retry: false,
  });
  const graph = useQuery({
    queryKey: ["graph-local", noteKey],
    enabled: !!q.data,
    queryFn: async () => api().vault.graph({ centerId: q.data!.ref?.id ?? noteKey, depth: 1, limit: 60 }),
  });
  if (q.isLoading)
    return (
      <div className="space-y-3 p-6">
        <Skeleton className="h-7 w-1/2" />
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-40" />
      </div>
    );
  if (q.isError || !q.data)
    return (
      <EmptyState
        icon={<FileText />}
        title={`No note “${noteKey}”`}
        body="This wikilink points to a note that doesn't exist yet."
      />
    );
  const n = q.data;
  const fm = Object.entries(n.frontmatter ?? {}).filter(([k]) => !["title", "tags", "type"].includes(k));
  const lnodes: GraphNode[] = (graph.data?.nodes ?? []).map((r) => ({ id: r.id, title: r.title, group: noteGroup(r.path) }));
  const llinks = (graph.data?.edges ?? []).map((e) => ({ source: e.sourceId, target: e.targetId }));
  return (
    <div className="grid gap-5 2xl:grid-cols-[minmax(0,1fr)_320px]">
      <Card className="min-w-0 p-5 sm:p-7">
        <div className="mb-4 flex flex-wrap items-center gap-2 text-xs text-muted">
          <span className="inline-flex items-center gap-1">
            <span className="size-2 rounded-full" style={{ background: groupColor(noteGroup(n.ref?.path ?? "")) }} />
            {n.type || noteGroup(n.ref?.path ?? "")}
          </span>
          <span className="font-mono">{n.ref?.path}</span>
          <span>· updated {formatDateTime(tsDate(n.updatedAt))}</span>
          {n.commit && <span className="font-mono">· {n.commit.slice(0, 7)}</span>}
        </div>
        {n.tags.length > 0 && (
          <div className="mb-3 flex flex-wrap gap-1">
            {n.tags.map((t) => (
              <Badge key={t} tone="accent">
                <Hash className="size-3" />
                {t}
              </Badge>
            ))}
          </div>
        )}
        {fm.length > 0 && (
          <div className="mb-5 grid gap-x-4 gap-y-1 rounded-lg border border-border bg-surface-2/60 p-3 text-[12.5px] sm:grid-cols-[140px_minmax(0,1fr)]">
            {fm.map(([k, v]) => (
              <div key={k} className="contents">
                <div className="font-mono text-muted">{k}</div>
                <div className="min-w-0 font-mono break-words text-fg-2">{typeof v === "string" ? v : JSON.stringify(v)}</div>
              </div>
            ))}
          </div>
        )}
        <Markdown known={known}>{n.body}</Markdown>
      </Card>
      <div className="space-y-5">
        <Card>
          <CardHeader icon={<ArrowUpRight className="size-4" />} title="Backlinks" subtitle={`${n.backlinks.length} notes link here`} />
          <div className="p-1.5">
            {n.backlinks.length === 0 && <div className="px-3 py-4 text-xs text-muted">No backlinks yet.</div>}
            {n.backlinks.map((b) => (
              <button key={b.id} onClick={() => onOpen(b.id)} className="flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-[13px] text-fg-2 hover:bg-surface-2 hover:text-fg">
                <span className="size-2 shrink-0 rounded-full" style={{ background: groupColor(noteGroup(b.path)) }} />
                <span className="truncate">{b.title || b.path}</span>
              </button>
            ))}
          </div>
        </Card>
        <Card>
          <CardHeader icon={<Link2 className="size-4" />} title="Links" subtitle={`${n.links.length} outgoing`} />
          <div className="p-1.5">
            {n.links.map((b) => (
              <button key={b.id} onClick={() => onOpen(b.id)} className="flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-[13px] text-fg-2 hover:bg-surface-2 hover:text-fg">
                <span className="size-2 shrink-0 rounded-full" style={{ background: groupColor(noteGroup(b.path)) }} />
                <span className="truncate">{b.title || b.path}</span>
              </button>
            ))}
            {n.links.length === 0 && <div className="px-3 py-4 text-xs text-muted">No outgoing links.</div>}
          </div>
        </Card>
        {lnodes.length > 1 && (
          <Card className="overflow-hidden">
            <CardHeader icon={<Network className="size-4" />} title="Local graph" />
            <ForceGraph nodes={lnodes} links={llinks} selectedId={n.ref?.id} onSelect={onOpen} height={260} className="rounded-none" />
          </Card>
        )}
      </div>
    </div>
  );
}

export default function Vault() {
  const [params, setParams] = useSearchParams();
  const noteKey = params.get("note") ?? "";
  const view = (params.get("view") as "note" | "graph") || (noteKey ? "note" : "graph");
  const [query, setQuery] = useState("");
  const dq = useDebounced(query.trim(), 200);
  const [hidden, setHidden] = useState<Set<string>>(new Set(["template", "policy"]));

  const graph = useQuery({ queryKey: qk.graph, queryFn: async () => api().vault.graph({ depth: 0, limit: 500 }) });
  const search = useQuery({
    queryKey: qk.search(dq),
    enabled: dq.length >= 2,
    queryFn: async () => (await api().vault.search({ query: dq, limit: 20 })).hits,
  });

  const open = (id: string) => setParams({ note: id, view: "note" });
  const setView = (v: "note" | "graph") => {
    const n = new URLSearchParams(params);
    n.set("view", v);
    setParams(n);
  };

  const refs = graph.data?.nodes ?? [];
  const known = useMemo(() => {
    const s = new Set<string>();
    for (const r of refs) {
      s.add(r.title.toLowerCase());
      s.add(r.id.toLowerCase());
      s.add((r.path.split("/").pop() ?? "").replace(/\.md$/, "").toLowerCase());
    }
    return s;
  }, [refs]);
  const groups = useMemo(() => [...new Set(refs.map((r) => noteGroup(r.path)))].sort(), [refs]);
  const gnodes = useMemo<GraphNode[]>(() => refs.map((r) => ({ id: r.id, title: r.title, group: noteGroup(r.path) })).filter((n) => !hidden.has(n.group)), [refs, hidden]);
  const glinks = useMemo(() => {
    const ids = new Set(gnodes.map((n) => n.id));
    return (graph.data?.edges ?? []).filter((e) => ids.has(e.sourceId) && ids.has(e.targetId)).map((e) => ({ source: e.sourceId, target: e.targetId }));
  }, [graph.data, gnodes]);
  const selectedId = refs.find((r) => r.id === noteKey || r.title === noteKey || r.path === noteKey)?.id;

  return (
    <div>
      <PageHeader
        icon={<Network />}
        title="Vault"
        subtitle={`${refs.length} notes · ${graph.data?.edges.length ?? 0} links`}
        actions={
          <Tabs<"note" | "graph">
            value={view}
            onChange={setView}
            items={[
              { id: "graph", label: "Graph" },
              { id: "note", label: "Note" },
            ]}
          />
        }
      />
      <div className="grid gap-5 lg:grid-cols-[260px_minmax(0,1fr)]">
        <Card className="h-fit overflow-hidden lg:sticky lg:top-0">
          <div className="border-b border-border p-2.5">
            <div className="relative">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted" />
              <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search the vault…" className="pr-8 pl-8" />
              {query && (
                <button onClick={() => setQuery("")} className="absolute top-1/2 right-2 -translate-y-1/2 text-muted hover:text-fg" aria-label="Clear">
                  <X className="size-3.5" />
                </button>
              )}
            </div>
          </div>
          <div className="max-h-[40vh] overflow-y-auto scrollbar-thin lg:max-h-[calc(100dvh-220px)]">
            {dq.length >= 2 ? (
              search.isLoading ? (
                <SkeletonRows rows={4} />
              ) : (search.data ?? []).length === 0 ? (
                <div className="p-6 text-center text-xs text-muted">No results for “{dq}”.</div>
              ) : (
                <div className="p-1.5">
                  {search.data!.map((h) => (
                    <button key={h.note?.id} onClick={() => open(h.note!.id)} className="block w-full rounded-lg px-2.5 py-2 text-left hover:bg-surface-2">
                      <div className="flex items-center gap-1.5 text-[13px] font-medium text-fg">
                        <span className="size-2 rounded-full" style={{ background: groupColor(noteGroup(h.note?.path ?? "")) }} />
                        <Highlight text={h.note?.title ?? ""} query={dq} />
                      </div>
                      <div className="mt-0.5 line-clamp-2 text-xs text-muted">
                        <Highlight text={h.snippet.replace(/[#*>[\]]/g, "")} query={dq} />
                      </div>
                    </button>
                  ))}
                </div>
              )
            ) : graph.isLoading ? (
              <SkeletonRows rows={6} />
            ) : graph.isError ? (
              <ErrorState compact error={graph.error} onRetry={() => graph.refetch()} />
            ) : (
              <NoteList refs={refs.filter((r) => !r.path.startsWith("templates/"))} selected={selectedId} onSelect={open} />
            )}
          </div>
        </Card>

        <div className="min-w-0">
          {view === "graph" ? (
            <Card className="overflow-hidden">
              <div className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
                <span className="text-[13px] font-semibold text-fg">Knowledge graph</span>
                <span className="text-xs text-muted">drag · scroll to zoom · click to open</span>
                <span className="flex-1" />
                <GraphLegend
                  groups={groups}
                  active={new Set(groups.filter((g) => !hidden.has(g)))}
                  onToggle={(g) => setHidden((s) => { const n = new Set(s); if (n.has(g)) n.delete(g); else n.add(g); return n; })}
                />
              </div>
              {graph.isLoading ? (
                <Skeleton className="m-4 h-[540px]" />
              ) : graph.isError ? (
                <ErrorState error={graph.error} onRetry={() => graph.refetch()} />
              ) : (
                <ForceGraph nodes={gnodes} links={glinks} selectedId={selectedId} onSelect={open} height={600} className="rounded-none" />
              )}
            </Card>
          ) : noteKey ? (
            <NoteView key={noteKey} noteKey={noteKey} known={known} onOpen={open} />
          ) : (
            <Card>
              <EmptyState icon={<FileText />} title="Pick a note" body="Search or choose a note from the list, or explore the graph." />
            </Card>
          )}
        </div>
      </div>
      <div className="mt-4 text-xs text-muted">
        Notes are plain Markdown in your vault (git). <Link to="/agents" className="text-accent hover:underline">Agents</Link> are notes too.
      </div>
    </div>
  );
}
