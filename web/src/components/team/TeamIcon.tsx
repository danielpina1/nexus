import { Bot, Code2, MessagesSquare, Search, ShoppingCart, Users } from "lucide-react";
import { agentColor } from "../agentMeta";
import { cx } from "../../lib/format";

/** Team notes use SF Symbol names (shared with iOS); map the common ones to Lucide. */
function iconFor(name: string, id: string): React.ReactNode {
  const n = `${name} ${id}`.toLowerCase();
  if (/cart|buy|shop/.test(n)) return <ShoppingCart />;
  if (/hammer|dev|code|chevron/.test(n)) return <Code2 />;
  if (/bubble|debate|panel/.test(n)) return <MessagesSquare />;
  if (/magnifying|search|research/.test(n)) return <Search />;
  if (/person|people|squad|team|crew/.test(n)) return <Users />;
  return <Bot />;
}

export function TeamIcon({ id, icon = "", size = "md", className }: { id: string; icon?: string; size?: "sm" | "md" | "lg"; className?: string }) {
  const glyph = iconFor(icon, id);
  const color = agentColor(`team:${id}`);
  const dims = size === "sm" ? "size-6 rounded-md [&_svg]:size-3.5" : size === "lg" ? "size-11 rounded-xl [&_svg]:size-5" : "size-8 rounded-lg [&_svg]:size-4";
  return (
    <div aria-hidden className={cx("flex shrink-0 items-center justify-center", dims, className)} style={{ color, background: `color-mix(in oklab, ${color} 14%, transparent)` }}>
      {glyph}
    </div>
  );
}
