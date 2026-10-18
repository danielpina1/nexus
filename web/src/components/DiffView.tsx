import { cx } from "../lib/format";

/** Renders a unified diff with +/- line tints (never colour alone: the sign stays visible). */
export function DiffView({ diff, className }: { diff: string; className?: string }) {
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <pre className={cx("overflow-x-auto rounded-lg border border-border bg-surface-2 py-2 font-mono text-[12px] leading-[19px] scrollbar-thin", className)}>
      {lines.map((l, i) => {
        const kind = l.startsWith("+++") || l.startsWith("---") ? "file" : l.startsWith("@@") ? "hunk" : l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : "ctx";
        return (
          <div
            key={i}
            className={cx(
              "min-w-max px-3 whitespace-pre",
              kind === "add" && "bg-ok-soft text-ok",
              kind === "del" && "bg-danger-soft text-danger",
              kind === "hunk" && "text-info",
              kind === "file" && "font-semibold text-fg-2",
              kind === "ctx" && "text-fg-2",
            )}
          >
            {l || " "}
          </div>
        );
      })}
    </pre>
  );
}
