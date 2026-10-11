import { useEffect, useMemo, useRef, useState } from "react";
import { Maximize2, Minus, Plus } from "lucide-react";
import { cx } from "../lib/format";
import { groupColor } from "./graphUtil";

export interface GraphNode {
  id: string;
  title: string;
  group: string;
}
export interface GraphLink {
  source: string;
  target: string;
}

interface Sim {
  x: number;
  y: number;
  vx: number;
  vy: number;
  fx?: number;
  fy?: number;
}

/**
 * Small dependency-free force-directed graph (repulsion + springs + centering) rendered to SVG.
 * Supports drag, wheel zoom, background pan and click-to-select.
 */
export function ForceGraph({
  nodes,
  links,
  selectedId,
  onSelect,
  className,
  height = 520,
  showLabels = true,
}: {
  nodes: GraphNode[];
  links: GraphLink[];
  selectedId?: string;
  onSelect?: (id: string) => void;
  className?: string;
  height?: number;
  showLabels?: boolean;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const sim = useRef(new Map<string, Sim>());
  const alpha = useRef(1);
  const raf = useRef(0);
  const [pos, setPos] = useState<Map<string, { x: number; y: number }>>(() => new Map());
  const [width, setWidth] = useState(0);
  const publish = () => setPos(new Map([...sim.current].map(([k, v]) => [k, { x: v.x, y: v.y }])));
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const [hover, setHover] = useState<string | null>(null);
  const drag = useRef<{ id?: string; px: number; py: number; vx: number; vy: number; moved: boolean } | null>(null);

  const degree = useMemo(() => {
    const d = new Map<string, number>();
    for (const l of links) {
      d.set(l.source, (d.get(l.source) ?? 0) + 1);
      d.set(l.target, (d.get(l.target) ?? 0) + 1);
    }
    return d;
  }, [links]);

  const neighbors = useMemo(() => {
    const focus = hover ?? selectedId;
    if (!focus) return null;
    const s = new Set([focus]);
    for (const l of links) {
      if (l.source === focus) s.add(l.target);
      if (l.target === focus) s.add(l.source);
    }
    return s;
  }, [hover, selectedId, links]);

  // (Re)seed positions for new nodes and restart the simulation.
  useEffect(() => {
    const m = sim.current;
    const ids = new Set(nodes.map((n) => n.id));
    for (const id of [...m.keys()]) if (!ids.has(id)) m.delete(id);
    nodes.forEach((n, i) => {
      if (!m.has(n.id)) {
        const a = i * 2.399963; // golden angle spiral
        const r = 18 * Math.sqrt(i + 1);
        m.set(n.id, { x: Math.cos(a) * r, y: Math.sin(a) * r, vx: 0, vy: 0 });
      }
    });
    alpha.current = 1;
    const step = () => {
      tick(m, links, degree);
      alpha.current *= 0.985;
      publish();
      if (alpha.current > 0.02) raf.current = requestAnimationFrame(step);
    };
    cancelAnimationFrame(raf.current);
    raf.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf.current);

    function tick(mm: Map<string, Sim>, ls: GraphLink[], deg: Map<string, number>) {
      const arr = [...mm.entries()];
      const a = alpha.current;
      // repulsion
      for (let i = 0; i < arr.length; i++) {
        const [, p] = arr[i];
        for (let j = i + 1; j < arr.length; j++) {
          const [, q] = arr[j];
          let dx = p.x - q.x;
          let dy = p.y - q.y;
          let d2 = dx * dx + dy * dy;
          if (d2 < 1) {
            dx = Math.random() - 0.5;
            dy = Math.random() - 0.5;
            d2 = 1;
          }
          const f = (5200 * a) / d2;
          const d = Math.sqrt(d2);
          const fx = (dx / d) * f;
          const fy = (dy / d) * f;
          p.vx += fx;
          p.vy += fy;
          q.vx -= fx;
          q.vy -= fy;
        }
      }
      // springs
      for (const l of ls) {
        const p = mm.get(l.source);
        const q = mm.get(l.target);
        if (!p || !q) continue;
        const dx = q.x - p.x;
        const dy = q.y - p.y;
        const d = Math.sqrt(dx * dx + dy * dy) || 1;
        const target = 95 + 8 * Math.min(6, (deg.get(l.source) ?? 0) + (deg.get(l.target) ?? 0));
        const f = ((d - target) / d) * 0.06 * a;
        p.vx += dx * f;
        p.vy += dy * f;
        q.vx -= dx * f;
        q.vy -= dy * f;
      }
      // gravity + integrate
      for (const [, p] of arr) {
        p.vx -= p.x * 0.008 * a;
        p.vy -= p.y * 0.008 * a;
        if (p.fx != null) {
          p.x = p.fx;
          p.y = p.fy!;
          p.vx = p.vy = 0;
          continue;
        }
        p.vx *= 0.6;
        p.vy *= 0.6;
        p.x += Math.max(-30, Math.min(30, p.vx));
        p.y += Math.max(-30, Math.min(30, p.vy));
      }
    }
  }, [nodes, links, degree]);

  const reheat = () => {
    if (alpha.current < 0.3) {
      alpha.current = 0.3;
      cancelAnimationFrame(raf.current);
      const m = sim.current;
      const step = () => {
        // light re-run of the same integrator (springs/gravity only matter for the dragged neighbourhood)
        for (const l of links) {
          const p = m.get(l.source);
          const q = m.get(l.target);
          if (!p || !q) continue;
          const dx = q.x - p.x;
          const dy = q.y - p.y;
          const d = Math.sqrt(dx * dx + dy * dy) || 1;
          const f = ((d - 90) / d) * 0.04 * alpha.current;
          if (p.fx == null) {
            p.x += dx * f;
            p.y += dy * f;
          }
          if (q.fx == null) {
            q.x -= dx * f;
            q.y -= dy * f;
          }
        }
        alpha.current *= 0.95;
        publish();
        if (alpha.current > 0.02) raf.current = requestAnimationFrame(step);
      };
      raf.current = requestAnimationFrame(step);
    }
  };

  const toWorld = (cx: number, cy: number) => {
    const r = svgRef.current!.getBoundingClientRect();
    return { x: (cx - r.left - r.width / 2 - view.x) / view.k, y: (cy - r.top - r.height / 2 - view.y) / view.k };
  };

  const onWheel = (e: React.WheelEvent) => {
    const r = svgRef.current!.getBoundingClientRect();
    const mx = e.clientX - r.left - r.width / 2;
    const my = e.clientY - r.top - r.height / 2;
    const k = Math.max(0.3, Math.min(3, view.k * Math.exp(-e.deltaY * 0.0015)));
    setView({ k, x: mx - ((mx - view.x) / view.k) * k, y: my - ((my - view.y) / view.k) * k });
  };

  const fit = () => {
    const pts = [...sim.current.values()];
    if (!pts.length || !svgRef.current) return;
    const r = svgRef.current.getBoundingClientRect();
    const xs = pts.map((p) => p.x);
    const ys = pts.map((p) => p.y);
    const w = Math.max(...xs) - Math.min(...xs) + 200;
    const h = Math.max(...ys) - Math.min(...ys) + 120;
    const k = Math.max(0.3, Math.min(1.1, Math.min(r.width / w, r.height / h)));
    const cxm = (Math.max(...xs) + Math.min(...xs)) / 2;
    const cym = (Math.max(...ys) + Math.min(...ys)) / 2;
    setView({ k, x: -cxm * k, y: -cym * k });
  };

  // Fit once the layout has mostly settled.
  const fitRef = useRef(fit);
  useEffect(() => {
    fitRef.current = fit;
  });
  useEffect(() => {
    const t = setTimeout(() => fitRef.current(), 1600);
    return () => clearTimeout(t);
  }, [nodes.length]);

  useEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div className={cx("relative overflow-hidden rounded-xl bg-surface-2/40", className)} style={{ height }}>
      <svg
        ref={svgRef}
        className="size-full touch-none select-none"
        onWheel={onWheel}
        onPointerDown={(e) => {
          (e.target as Element).setPointerCapture?.(e.pointerId);
          const nodeId = (e.target as Element).closest("[data-node]")?.getAttribute("data-node") ?? undefined;
          drag.current = { id: nodeId, px: e.clientX, py: e.clientY, vx: view.x, vy: view.y, moved: false };
          if (nodeId) {
            const p = sim.current.get(nodeId);
            if (p) {
              p.fx = p.x;
              p.fy = p.y;
            }
          }
        }}
        onPointerMove={(e) => {
          const d = drag.current;
          if (!d) return;
          if (Math.abs(e.clientX - d.px) + Math.abs(e.clientY - d.py) > 3) d.moved = true;
          if (d.id) {
            const p = sim.current.get(d.id);
            if (p) {
              const w = toWorld(e.clientX, e.clientY);
              p.fx = p.x = w.x;
              p.fy = p.y = w.y;
              reheat();
              publish();
            }
          } else {
            setView((v) => ({ ...v, x: d.vx + e.clientX - d.px, y: d.vy + e.clientY - d.py }));
          }
        }}
        onPointerUp={() => {
          const d = drag.current;
          drag.current = null;
          if (d?.id) {
            const p = sim.current.get(d.id);
            if (p) {
              p.fx = undefined;
              p.fy = undefined;
            }
            if (!d.moved) onSelect?.(d.id);
          }
        }}
      >
        <g transform={`translate(${width / 2 + view.x} ${height / 2 + view.y}) scale(${view.k})`}>
          {links.map((l, i) => {
            const p = pos.get(l.source);
            const q = pos.get(l.target);
            if (!p || !q) return null;
            const on = neighbors ? neighbors.has(l.source) && neighbors.has(l.target) : false;
            return (
              <line
                key={i}
                x1={p.x}
                y1={p.y}
                x2={q.x}
                y2={q.y}
                stroke={on ? "var(--accent)" : "var(--border-strong)"}
                strokeOpacity={neighbors && !on ? 0.25 : on ? 0.9 : 0.7}
                strokeWidth={on ? 1.6 : 1}
              />
            );
          })}
          {nodes.map((n) => {
            const p = pos.get(n.id);
            if (!p) return null;
            const r = (4.5 + Math.min(8, Math.sqrt(degree.get(n.id) ?? 0) * 2.2)) / Math.sqrt(Math.max(1, view.k));
            const sel = n.id === selectedId;
            const dim = neighbors && !neighbors.has(n.id);
            const label = showLabels && (view.k > 0.9 || sel || (degree.get(n.id) ?? 0) >= 3 || neighbors?.has(n.id));
            return (
              <g
                key={n.id}
                data-node={n.id}
                transform={`translate(${p.x} ${p.y})`}
                className="cursor-pointer"
                opacity={dim ? 0.3 : 1}
                onPointerEnter={() => setHover(n.id)}
                onPointerLeave={() => setHover(null)}
              >
                <circle r={r + 6} fill="transparent" />
                <circle r={r} fill={groupColor(n.group)} stroke={sel ? "var(--fg)" : "var(--surface)"} strokeWidth={(sel ? 2.5 : 2) / Math.max(1, view.k)} />
                {label && (
                  <text y={r + 12 / view.k} textAnchor="middle" fontSize={11 / view.k} fill="var(--fg-2)" stroke="var(--bg)" strokeWidth={3 / view.k} paintOrder="stroke" style={{ pointerEvents: "none" }}>
                    {n.title.length > 26 ? n.title.slice(0, 25) + "…" : n.title}
                  </text>
                )}
              </g>
            );
          })}
        </g>
      </svg>
      <div className="absolute right-2 bottom-2 flex flex-col overflow-hidden rounded-lg border border-border bg-surface shadow-card">
        <button className="p-1.5 text-muted hover:bg-surface-2 hover:text-fg" aria-label="Zoom in" onClick={() => setView((v) => ({ ...v, k: Math.min(3, v.k * 1.25) }))}>
          <Plus className="size-3.5" />
        </button>
        <button className="p-1.5 text-muted hover:bg-surface-2 hover:text-fg" aria-label="Zoom out" onClick={() => setView((v) => ({ ...v, k: Math.max(0.3, v.k / 1.25) }))}>
          <Minus className="size-3.5" />
        </button>
        <button className="p-1.5 text-muted hover:bg-surface-2 hover:text-fg" aria-label="Fit" onClick={fit}>
          <Maximize2 className="size-3.5" />
        </button>
      </div>
    </div>
  );
}

export function GraphLegend({ groups, active, onToggle }: { groups: string[]; active?: Set<string>; onToggle?: (g: string) => void }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {groups.map((g) => {
        const on = !active || active.has(g);
        return (
          <button
            key={g}
            onClick={() => onToggle?.(g)}
            className={cx("inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs transition-opacity", on ? "border-border bg-surface text-fg-2" : "border-transparent text-muted opacity-60")}
          >
            <span className="size-2 rounded-full" style={{ background: groupColor(g) }} />
            {g}
          </button>
        );
      })}
    </div>
  );
}
