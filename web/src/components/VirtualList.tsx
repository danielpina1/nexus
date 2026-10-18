import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

/**
 * Windowed list (TanStack Virtual). Short lists render normally; past `threshold` items only the
 * visible rows (plus overscan) are in the DOM. Rows are measured, so variable heights are fine.
 *
 * - `scrollRef` omitted: scrolls with the page's main scroller (`#main`).
 * - `scrollRef` given: scrolls inside that element.
 */
export function VirtualList<T>({
  items,
  getKey,
  render,
  estimate = 56,
  threshold = 80,
  scrollRef,
  className,
}: {
  items: T[];
  getKey: (item: T, index: number) => string;
  render: (item: T, index: number) => ReactNode;
  estimate?: number;
  threshold?: number;
  scrollRef?: React.RefObject<HTMLElement | null>;
  className?: string;
}) {
  if (items.length <= threshold) {
    return (
      <div className={className}>
        {items.map((it, i) => (
          <div key={getKey(it, i)}>{render(it, i)}</div>
        ))}
      </div>
    );
  }
  return <Windowed items={items} getKey={getKey} render={render} estimate={estimate} scrollRef={scrollRef} className={className} />;
}

function Windowed<T>({
  items,
  getKey,
  render,
  estimate,
  scrollRef,
  className,
}: {
  items: T[];
  getKey: (item: T, index: number) => string;
  render: (item: T, index: number) => ReactNode;
  estimate: number;
  scrollRef?: React.RefObject<HTMLElement | null>;
  className?: string;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const [margin, setMargin] = useState(0);
  const scrollEl = () => scrollRef?.current ?? document.getElementById("main");
  useLayoutEffect(() => {
    const el = scrollEl();
    const list = listRef.current;
    if (!el || !list) return;
    const measure = () => setMargin(list.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual is the windowing lib; rows re-render on scroll by design.
  const v = useVirtualizer({
    count: items.length,
    getScrollElement: scrollEl,
    estimateSize: () => estimate,
    overscan: 10,
    scrollMargin: margin,
    getItemKey: (i) => getKey(items[i], i),
  });
  const rows = v.getVirtualItems();
  return (
    <div ref={listRef} style={{ height: v.getTotalSize(), position: "relative" }}>
      <div className={className} style={{ position: "absolute", top: 0, left: 0, right: 0, transform: `translateY(${(rows[0]?.start ?? 0) - margin}px)` }}>
        {rows.map((r) => (
          <div key={r.key} data-index={r.index} ref={v.measureElement}>
            {render(items[r.index], r.index)}
          </div>
        ))}
      </div>
    </div>
  );
}
