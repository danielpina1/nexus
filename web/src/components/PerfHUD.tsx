import { useEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { perfSample, type PerfSample } from "../lib/perf";
import { useEvents } from "../lib/store";
import { useUI } from "../lib/ui";
import { cx } from "../lib/format";

interface Stats extends PerfSample {
  fps: number;
  /** Worst frame time in the last second (ms). */
  worstFrame: number;
  tasks: number;
  timelineEvents: number;
  dom: number;
  heapMb?: number;
}

/** Tiny dev HUD (toggle with ` or ⌘K → "Toggle performance HUD"). Lazy-loaded; costs nothing when closed. */
export default function PerfHUD() {
  const [s, setS] = useState<Stats | null>(null);
  const frames = useRef<number[]>([]);
  useEffect(() => {
    let raf = 0;
    let last = performance.now();
    const loop = (t: number) => {
      frames.current.push(t - last);
      last = t;
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    const iv = setInterval(() => {
      const f = frames.current;
      frames.current = [];
      const total = f.reduce((a, b) => a + b, 0);
      const st = useEvents.getState();
      let tl = 0;
      for (const k in st.timelines) tl += st.timelines[k].length;
      const mem = (performance as unknown as { memory?: { usedJSHeapSize: number } }).memory;
      setS({
        ...perfSample(),
        fps: total ? Math.round((f.length * 1000) / total) : 0,
        worstFrame: f.length ? Math.max(...f) : 0,
        tasks: Object.keys(st.tasks).length,
        timelineEvents: tl,
        dom: document.getElementsByTagName("*").length,
        heapMb: mem ? mem.usedJSHeapSize / 1048576 : undefined,
      });
    }, 1000);
    return () => {
      cancelAnimationFrame(raf);
      clearInterval(iv);
    };
  }, []);
  // Expose for e2e/perf scripts.
  useEffect(() => {
    (window as unknown as { __nexusHud?: Stats | null }).__nexusHud = s;
  }, [s]);

  const row = (k: string, v: string, warn?: boolean) => (
    <div className="flex justify-between gap-4">
      <span className="text-muted">{k}</span>
      <span className={cx("tabular", warn ? "text-warn" : "text-fg")}>{v}</span>
    </div>
  );
  return (
    <div
      role="status"
      aria-label="Performance"
      data-testid="perf-hud"
      className="pointer-events-auto fixed top-3 right-3 z-[70] w-52 rounded-lg border border-border bg-surface/95 p-2.5 font-mono text-[11px] leading-[18px] shadow-pop backdrop-blur"
    >
      <div className="mb-1 flex items-center justify-between">
        <span className="font-semibold text-fg">perf</span>
        <button onClick={() => useUI.getState().togglePerf()} className="text-muted hover:text-fg" aria-label="Close performance HUD">
          <X className="size-3.5" />
        </button>
      </div>
      {s ? (
        <>
          {row("fps", String(s.fps), s.fps < 50)}
          {row("worst frame", `${s.worstFrame.toFixed(1)} ms`, s.worstFrame > 34)}
          {row("events/s", String(s.eventsPerSec))}
          {row("flushes/s", String(s.flushesPerSec))}
          {row("ingest avg", `${s.ingestAvgMs.toFixed(2)} ms`, s.ingestAvgMs > 4)}
          {row("ingest max", `${s.ingestMaxMs.toFixed(2)} ms`, s.ingestMaxMs > 8)}
          {row("events total", String(s.totalEvents))}
          {row("tasks", String(s.tasks))}
          {row("timeline evts", String(s.timelineEvents))}
          {row("dom nodes", String(s.dom), s.dom > 6000)}
          {s.heapMb != null && row("heap", `${s.heapMb.toFixed(1)} MB`)}
        </>
      ) : (
        <div className="text-muted">measuring…</div>
      )}
    </div>
  );
}
