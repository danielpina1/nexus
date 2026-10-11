import { useEffect, useMemo, useRef, useState } from "react";
import { Coins, Leaf, Table2, BarChart3, Zap } from "lucide-react";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Tabs } from "../components/ui/Tabs";
import { EmptyState } from "../components/ui/EmptyState";
import { agentColor, agentName, agentSlot, useAgentMap } from "../components/agentMeta";
import { startOfDay, useCostRows, type CostRow } from "../lib/costs";
import { cx, formatMoney, formatTokens } from "../lib/format";
import { useNow } from "../lib/useNow";

const DAY = 86_400_000;

interface DayStack {
  day: number;
  total: number;
  parts: Array<{ agentId: string; minor: number }>;
}

function niceMax(v: number): number {
  if (v <= 0) return 100;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

function StackedBars({ data, agents, names }: { data: DayStack[]; agents: string[]; names: (id: string) => string }) {
  const [hover, setHover] = useState<number | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(760);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(Math.max(320, el.clientWidth)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Draw at 1:1 so axis text stays at its real size at any width.
  const W = width;
  const H = 280;
  const pad = { l: 48, r: 8, t: 12, b: 28 };
  const max = niceMax(Math.max(...data.map((d) => d.total)));
  const iw = W - pad.l - pad.r;
  const ih = H - pad.t - pad.b;
  const bw = iw / data.length;
  const barW = Math.min(34, bw * 0.62);
  const y = (v: number) => pad.t + ih - (v / max) * ih;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
  const h = hover != null ? data[hover] : null;
  return (
    <div className="relative" ref={box}>
      <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H} className="block max-w-full" role="img" aria-label="Daily spend by agent" onMouseLeave={() => setHover(null)}>
        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeDasharray={t === 0 ? undefined : "2 4"} />
            <text x={pad.l - 8} y={y(t) + 4} textAnchor="end" fontSize={11} fill="var(--muted)" className="tabular">
              {formatMoney(t, "EUR", { compact: true })}
            </text>
          </g>
        ))}
        {data.map((d, i) => {
          const x = pad.l + i * bw + (bw - barW) / 2;
          let acc = 0;
          const top = y(d.total);
          const clip = `clip-${i}`;
          return (
            <g key={d.day} onMouseEnter={() => setHover(i)}>
              <rect x={pad.l + i * bw} y={pad.t} width={bw} height={ih} fill={hover === i ? "var(--surface-2)" : "transparent"} />
              <clipPath id={clip}>
                <rect x={x} y={top} width={barW} height={Math.max(0, y(0) - top) + 4} rx={4} />
              </clipPath>
              <g clipPath={`url(#${clip})`}>
                {d.parts.map((p) => {
                  const y0 = y(acc);
                  acc += p.minor;
                  const y1 = y(acc);
                  const hgt = Math.max(0, y0 - y1 - (acc < d.total ? 2 : 0));
                  return <rect key={p.agentId} x={x} y={y1} width={barW} height={hgt} fill={agentColor(p.agentId)} />;
                })}
              </g>
              {(data.length <= 8 || (bw >= 56 ? true : (data.length - 1 - i) % Math.ceil(56 / bw) === 0)) && (
                <text x={x + barW / 2} y={H - 8} textAnchor="middle" fontSize={11} fill="var(--muted)">
                  {new Date(d.day).toLocaleDateString(undefined, { day: "numeric", month: "short" })}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      {h && hover != null && (
        <div
          className="pointer-events-none absolute top-2 z-10 w-52 rounded-lg border border-border bg-surface p-2.5 text-xs shadow-pop"
          style={{ left: `clamp(0px, calc(${((hover + 0.5) / data.length) * 100}% - 104px), calc(100% - 208px))` }}
        >
          <div className="mb-1.5 flex justify-between font-medium text-fg">
            <span>{new Date(h.day).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" })}</span>
            <span className="tabular">{formatMoney(h.total)}</span>
          </div>
          {[...h.parts].reverse().map((p) => (
            <div key={p.agentId} className="flex items-center gap-1.5 py-0.5 text-fg-2">
              <span className="size-2 rounded-sm" style={{ background: agentColor(p.agentId) }} />
              <span className="flex-1 truncate">{names(p.agentId)}</span>
              <span className="tabular">{formatMoney(p.minor)}</span>
            </div>
          ))}
          {h.parts.length === 0 && <div className="text-muted">No spend</div>}
        </div>
      )}
      <div className="mt-2 flex flex-wrap gap-3 px-1">
        {agents.map((a) => (
          <span key={a} className="inline-flex items-center gap-1.5 text-xs text-fg-2">
            <span className="size-2.5 rounded-sm" style={{ background: agentColor(a) }} />
            {names(a)}
          </span>
        ))}
      </div>
    </div>
  );
}

function SplitBar({ local, cloud }: { local: number; cloud: number }) {
  const total = local + cloud || 1;
  const lp = (local / total) * 100;
  return (
    <div>
      <div className="flex h-3 w-full gap-[2px] overflow-hidden rounded-full">
        <div className="h-full rounded-l-full" style={{ width: `${lp}%`, background: "var(--series-3)" }} />
        <div className="h-full flex-1 rounded-r-full" style={{ background: "var(--series-1)" }} />
      </div>
      <div className="mt-2 flex justify-between text-xs text-fg-2">
        <span className="inline-flex items-center gap-1.5">
          <span className="size-2.5 rounded-sm" style={{ background: "var(--series-3)" }} /> Local {Math.round(lp)}%
        </span>
        <span className="inline-flex items-center gap-1.5">
          Cloud {Math.round(100 - lp)}% <span className="size-2.5 rounded-sm" style={{ background: "var(--series-1)" }} />
        </span>
      </div>
    </div>
  );
}

export default function Costs() {
  const rows = useCostRows();
  const agents = useAgentMap();
  const [days, setDays] = useState<"7" | "14" | "30">("14");
  const [mode, setMode] = useState<"chart" | "table">("chart");
  const n = Number(days);
  const now = useNow(60_000);
  const names = (id: string) => (id === "unknown" ? "Unattributed" : agentName(agents, id));

  const { stacks, inRange, agentIds } = useMemo(() => {
    const today = startOfDay(now);
    const first = today - (n - 1) * DAY;
    const inRange: CostRow[] = rows.filter((r) => r.at >= first);
    const stacks: DayStack[] = Array.from({ length: n }, (_, i) => ({ day: first + i * DAY, total: 0, parts: [] }));
    const ids = new Set<string>();
    for (const r of inRange) {
      const i = Math.round((startOfDay(r.at) - first) / DAY);
      const s = stacks[i];
      if (!s || r.minor === 0) continue;
      ids.add(r.agentId);
      s.total += r.minor;
      const p = s.parts.find((x) => x.agentId === r.agentId);
      if (p) p.minor += r.minor;
      else s.parts.push({ agentId: r.agentId, minor: r.minor });
    }
    const order = [...ids].sort((a, b) => agentSlot(a) - agentSlot(b));
    for (const s of stacks) s.parts.sort((a, b) => order.indexOf(a.agentId) - order.indexOf(b.agentId));
    return { stacks, inRange, agentIds: order };
  }, [rows, n, now]);

  const totals = useMemo(() => {
    let cloud = 0;
    let localTok = 0;
    let cloudTok = 0;
    const byAgent = new Map<string, { minor: number; tokens: number; events: number; tasks: Set<string> }>();
    const byModel = new Map<string, { minor: number; tokens: number; runtime: string; local: boolean }>();
    for (const r of inRange) {
      const tok = Number(r.cost.inputTokens + r.cost.outputTokens);
      if (r.local) localTok += tok;
      else {
        cloud += r.minor;
        cloudTok += tok;
      }
      const a = byAgent.get(r.agentId) ?? { minor: 0, tokens: 0, events: 0, tasks: new Set<string>() };
      a.minor += r.minor;
      a.tokens += tok;
      a.events++;
      a.tasks.add(r.taskId);
      byAgent.set(r.agentId, a);
      const key = `${r.cost.model || "—"}`;
      const m = byModel.get(key) ?? { minor: 0, tokens: 0, runtime: r.cost.runtime, local: r.local };
      m.minor += r.minor;
      m.tokens += tok;
      byModel.set(key, m);
    }
    return { cloud, localTok, cloudTok, byAgent, byModel };
  }, [inRange]);
  const today = stacks[stacks.length - 1]?.total ?? 0;
  const taskCount = new Set(inRange.map((r) => r.taskId)).size;

  return (
    <div>
      <PageHeader
        icon={<Coins />}
        title="Costs"
        subtitle="Spend from CostRecorded events, per agent, model and runtime."
        actions={
          <Tabs<"7" | "14" | "30">
            value={days}
            onChange={setDays}
            items={[
              { id: "7", label: "7 days" },
              { id: "14", label: "14 days" },
              { id: "30", label: "30 days" },
            ]}
          />
        }
      />
      {rows.length === 0 ? (
        <Card>
          <EmptyState icon={<Coins />} title="No cost events yet" body="Costs appear as agents run. Each run reports CostRecorded events on the stream." />
        </Card>
      ) : (
        <div className="space-y-5">
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <Card className="p-4">
              <div className="text-xs font-medium text-muted">Spend · {n} days</div>
              <div className="mt-2 text-2xl font-semibold tracking-tight text-fg tabular">{formatMoney(totals.cloud)}</div>
            </Card>
            <Card className="p-4">
              <div className="text-xs font-medium text-muted">Today</div>
              <div className="mt-2 text-2xl font-semibold tracking-tight text-fg tabular">{formatMoney(today)}</div>
            </Card>
            <Card className="p-4">
              <div className="text-xs font-medium text-muted">Avg per task</div>
              <div className="mt-2 text-2xl font-semibold tracking-tight text-fg tabular">{formatMoney(taskCount ? totals.cloud / taskCount : 0)}</div>
              <div className="text-xs text-muted">{taskCount} tasks</div>
            </Card>
            <Card className="p-4">
              <div className="flex items-center gap-1.5 text-xs font-medium text-muted">
                <Leaf className="size-3.5 text-ok" /> Local vs <Zap className="size-3.5" /> cloud tokens
              </div>
              <div className="mt-3">
                <SplitBar local={totals.localTok} cloud={totals.cloudTok} />
              </div>
            </Card>
          </div>

          <Card>
            <CardHeader
              title="Daily spend by agent"
              subtitle="Hover a day for the breakdown"
              actions={
                <div className="flex rounded-lg bg-surface-2 p-0.5">
                  <button onClick={() => setMode("chart")} className={cx("rounded-md p-1", mode === "chart" ? "bg-surface text-fg shadow-card" : "text-muted")} aria-label="Chart view">
                    <BarChart3 className="size-3.5" />
                  </button>
                  <button onClick={() => setMode("table")} className={cx("rounded-md p-1", mode === "table" ? "bg-surface text-fg shadow-card" : "text-muted")} aria-label="Table view">
                    <Table2 className="size-3.5" />
                  </button>
                </div>
              }
            />
            <div className="p-4">
              {mode === "chart" ? (
                <StackedBars data={stacks} agents={agentIds} names={names} />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-[13px] tabular">
                    <thead>
                      <tr className="text-left text-xs text-muted">
                        <th className="py-1.5 pr-4 font-medium">Day</th>
                        {agentIds.map((a) => (
                          <th key={a} className="py-1.5 pr-4 text-right font-medium">
                            {names(a)}
                          </th>
                        ))}
                        <th className="py-1.5 text-right font-medium">Total</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...stacks].reverse().map((s) => (
                        <tr key={s.day} className="border-t border-border/60">
                          <td className="py-1.5 pr-4 text-fg-2">{new Date(s.day).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" })}</td>
                          {agentIds.map((a) => (
                            <td key={a} className="py-1.5 pr-4 text-right text-fg-2">
                              {formatMoney(s.parts.find((p) => p.agentId === a)?.minor ?? 0)}
                            </td>
                          ))}
                          <td className="py-1.5 text-right font-medium text-fg">{formatMoney(s.total)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </Card>

          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <Card className="overflow-hidden">
              <CardHeader title="By agent" />
              <table className="w-full text-[13px] tabular">
                <thead>
                  <tr className="border-b border-border text-left text-xs text-muted">
                    <th className="px-4 py-2 font-medium">Agent</th>
                    <th className="px-4 py-2 text-right font-medium">Tasks</th>
                    <th className="px-4 py-2 text-right font-medium">Tokens</th>
                    <th className="px-4 py-2 text-right font-medium">Spend</th>
                  </tr>
                </thead>
                <tbody>
                  {[...totals.byAgent.entries()]
                    .sort((a, b) => b[1].minor - a[1].minor)
                    .map(([id, v]) => (
                      <tr key={id} className="border-b border-border/60 last:border-0">
                        <td className="px-4 py-2">
                          <span className="inline-flex items-center gap-2 text-fg">
                            <span className="size-2.5 rounded-sm" style={{ background: agentColor(id) }} />
                            {names(id)}
                          </span>
                        </td>
                        <td className="px-4 py-2 text-right text-fg-2">{v.tasks.size}</td>
                        <td className="px-4 py-2 text-right text-fg-2">{formatTokens(v.tokens)}</td>
                        <td className="px-4 py-2 text-right font-medium text-fg">{formatMoney(v.minor)}</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </Card>
            <Card className="overflow-hidden">
              <CardHeader title="By model" />
              <table className="w-full text-[13px] tabular">
                <thead>
                  <tr className="border-b border-border text-left text-xs text-muted">
                    <th className="px-4 py-2 font-medium">Model</th>
                    <th className="px-4 py-2 font-medium">Runtime</th>
                    <th className="px-4 py-2 text-right font-medium">Tokens</th>
                    <th className="px-4 py-2 text-right font-medium">Spend</th>
                  </tr>
                </thead>
                <tbody>
                  {[...totals.byModel.entries()]
                    .sort((a, b) => b[1].minor - a[1].minor || b[1].tokens - a[1].tokens)
                    .map(([model, v]) => (
                      <tr key={model} className="border-b border-border/60 last:border-0">
                        <td className="px-4 py-2 font-mono text-[12px] text-fg">{model}</td>
                        <td className="px-4 py-2 text-fg-2">
                          {v.runtime}
                          {v.local && <span className="ml-1.5 rounded bg-ok-soft px-1 text-[10px] text-ok">local</span>}
                        </td>
                        <td className="px-4 py-2 text-right text-fg-2">{formatTokens(v.tokens)}</td>
                        <td className="px-4 py-2 text-right font-medium text-fg">{formatMoney(v.minor)}</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </Card>
          </div>
        </div>
      )}
    </div>
  );
}
