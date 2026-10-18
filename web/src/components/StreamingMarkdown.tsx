import { memo, useMemo } from "react";
import { Markdown } from "./Markdown";
import { cx } from "../lib/format";

/**
 * Splits streamed Markdown into blocks at blank lines (outside code fences). Finished blocks never
 * change, so they are memoized and never re-parsed; only the growing tail is rendered again as
 * chunks arrive. This keeps a run with thousands of chunks at a steady frame rate.
 */
function splitBlocks(text: string): string[] {
  const blocks: string[] = [];
  let start = 0;
  let fence: string | null = null;
  let i = 0;
  while (i < text.length) {
    const nl = text.indexOf("\n", i);
    const end = nl === -1 ? text.length : nl;
    const line = text.slice(i, end);
    const m = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
    if (m) {
      if (!fence) fence = m[1][0];
      else if (m[1][0] === fence) fence = null;
    } else if (!fence && line.trim() === "" && end > start && nl !== -1) {
      const block = text.slice(start, end);
      if (block.trim()) blocks.push(block);
      start = nl + 1;
    }
    if (nl === -1) break;
    i = nl + 1;
  }
  const tail = text.slice(start);
  if (tail.trim() || blocks.length === 0) blocks.push(tail);
  return blocks;
}

const Block = memo(function Block({ md, className }: { md: string; className?: string }) {
  return <Markdown className={className}>{md}</Markdown>;
});

/** Very large tails (no blank lines for a long time) are shown as plain text until they settle. */
const PLAIN_TAIL = 20_000;

export function StreamingMarkdown({ text, streaming, className }: { text: string; streaming?: boolean; className?: string }) {
  const blocks = useMemo(() => splitBlocks(text), [text]);
  const last = blocks.length - 1;
  return (
    <div className={cx("stream-md", className)}>
      {blocks.map((b, i) =>
        i === last && streaming && b.length > PLAIN_TAIL ? (
          <pre key={i} className="font-sans text-[inherit] whitespace-pre-wrap text-fg-2">
            {b}
          </pre>
        ) : (
          <Block key={i} md={b} className={i === last && streaming ? "caret-block" : undefined} />
        ),
      )}
    </div>
  );
}
