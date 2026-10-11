import { Bot, Coins, Cpu, Inbox, LayoutDashboard, ListChecks, MessageSquare, Network, Settings, Shield } from "lucide-react";
import type { LucideIcon } from "lucide-react";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  key: string; // "g <key>" shortcut
}

export const NAV: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard, key: "o" },
  { to: "/inbox", label: "Inbox", icon: Inbox, key: "i" },
  { to: "/chat", label: "Chat", icon: MessageSquare, key: "c" },
  { to: "/tasks", label: "Tasks", icon: ListChecks, key: "t" },
  { to: "/agents", label: "Agents", icon: Bot, key: "a" },
  { to: "/vault", label: "Vault", icon: Network, key: "v" },
  { to: "/machines", label: "Machines", icon: Cpu, key: "m" },
  { to: "/costs", label: "Costs", icon: Coins, key: "$" },
  { to: "/security", label: "Security", icon: Shield, key: "s" },
  { to: "/settings", label: "Settings", icon: Settings, key: "," },
];
