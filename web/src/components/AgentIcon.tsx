import { Bot, BookOpen, Hammer, Server, ShoppingCart, Sparkles, Workflow, Globe, Terminal, Brain } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { agentColor } from "./agentMeta";
import { cx } from "../lib/format";

const ICONS: Record<string, LucideIcon> = {
  cart: ShoppingCart,
  "shopping-cart": ShoppingCart,
  hammer: Hammer,
  server: Server,
  books: BookOpen,
  book: BookOpen,
  sparkles: Sparkles,
  workflow: Workflow,
  globe: Globe,
  terminal: Terminal,
  brain: Brain,
};

const BY_AGENT: Record<string, string> = {
  assistant: "sparkles",
  "code-fixer": "hammer",
  "grocery-continente": "cart",
  ops: "server",
  librarian: "books",
};

export function AgentIcon({
  agentId,
  icon,
  size = "md",
  workflow,
  className,
}: {
  agentId: string;
  icon?: string;
  size?: "sm" | "md" | "lg";
  workflow?: boolean;
  className?: string;
}) {
  const Icon = workflow && !agentId ? Workflow : (ICONS[icon || BY_AGENT[agentId] || ""] ?? Bot);
  const color = agentId ? agentColor(agentId) : "var(--muted)";
  const dims = size === "sm" ? "size-6 rounded-md [&_svg]:size-3.5" : size === "lg" ? "size-11 rounded-xl [&_svg]:size-5" : "size-8 rounded-lg [&_svg]:size-4";
  return (
    <div
      className={cx("flex shrink-0 items-center justify-center", dims, className)}
      style={{ color, background: `color-mix(in oklab, ${color} 14%, transparent)` }}
    >
      <Icon />
    </div>
  );
}
