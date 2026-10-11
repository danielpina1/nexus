/** Fixed categorical slot per note type; unknown types fall back to gray. */
export const GROUP_SLOT: Record<string, number> = {
  agent: 1,
  person: 2,
  topic: 3,
  machine: 4,
  product: 5,
  episode: 6,
  project: 7,
};

export function groupColor(group: string): string {
  const s = GROUP_SLOT[group];
  return s ? `var(--series-${s})` : "var(--muted)";
}

const DIRS: Record<string, string> = {
  agents: "agent",
  people: "person",
  topics: "topic",
  projects: "project",
  machines: "machine",
  products: "product",
  episodes: "episode",
  daily: "daily",
  workflows: "workflow",
  policies: "policy",
  templates: "template",
  inbox: "inbox",
};

export function noteGroup(path: string): string {
  const dir = path.split("/")[0];
  return DIRS[dir] ?? (path.includes("/") ? dir : "note");
}
