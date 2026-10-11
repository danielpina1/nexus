import { splitFrontmatter, asString, asStringList, type YValue } from "../lib/yaml";

// The real starter vault, bundled at build time (only into the lazily loaded mock chunk).
const templateFiles = import.meta.glob("../../../vault-template/**/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

export interface MockNote {
  id: string; // path without .md
  path: string;
  title: string;
  type: string;
  tags: string[];
  frontmatter: Record<string, YValue>;
  body: string;
  raw: string;
  updatedAt: number;
}

const DAY = 86_400_000;

const extra: Record<string, string> = {
  "topics/Shopping preferences.md": `---
type: topic
title: Shopping preferences
tags: [shopping, preferences]
---

# Shopping preferences

- Store:: [[Continente]] (home delivery, Saturday morning slots)
- Budget per order:: €160, alert above €180
- Dietary rules:: lactose-free for [[Ana Pina]]; no palm oil
- Preferred brands:: [[Leite Mimosa meio-gordo]], [[Café Delta Lote Chávena]], [[Pão de forma Bimbo]]
- Substitutions allowed:: same brand other size; otherwise ask

Lessons from past runs live in the [[Grocery (Continente)]] agent note.
`,
  "topics/Continente.md": `---
type: org
title: Continente
tags: [shopping, store]
url: https://www.continente.pt
---

# Continente

Supermarket chain (part of [[Sonae]]). Online orders via continente.pt; account login is stored on
[[mac-studio]] in the browser profile used by the [[Grocery (Continente)]] agent.

Delivery slots fill up by Thursday for the weekend.
`,
  "topics/Sonae.md": `---
type: org
title: Sonae
tags: [company]
---

# Sonae

Retail group behind [[Continente]]. [[Rui Costa]] works here.
`,
  "people/Ana Pina.md": `---
type: person
title: Ana Pina
tags: [family]
---

# Ana Pina

- Relationship:: partner
- Diet:: lactose-free
- Likes:: [[Café Delta Lote Chávena]]

See [[Shopping preferences]].
`,
  "people/Rui Costa.md": `---
type: person
title: Rui Costa
tags: [work]
---

# Rui Costa

Friend from university, works at [[Sonae]]. Interested in [[nexus]] for home automation.
`,
  "projects/nexus.md": `---
type: project
title: nexus
tags: [code, personal]
repo: github.com/danielpina1/nexus
---

# nexus

Personal agent hub. Hub (\`nexusd\`) runs on [[vps]]; heavy coding agents run on [[mac-studio]];
local models on [[gpu-box]].

## Agents involved
- [[Code fixer]] — fixes and small features, asks before pushing
- [[Ops]] — keeps the machines healthy
- [[Librarian]] — files captures and episodes

## Recent
- [[2026-10-09 Fix flaky event-stream test]]
`,
  "machines/mac-studio.md": `---
type: machine
title: mac-studio
tags: [machine, dev, browser]
---

# mac-studio

Mac Studio M2 Max on the desk. Runs [[Code fixer]] (Claude Code, Codex) and the browser agent for
[[Continente]]. Always on; wakes on LAN.
`,
  "machines/vps.md": `---
type: machine
title: vps
tags: [machine, server]
---

# vps

Hetzner CX32 in Falkenstein. Hosts the Hub and backups. Managed by [[Ops]].
`,
  "machines/gpu-box.md": `---
type: machine
title: gpu-box
tags: [machine, gpu]
---

# gpu-box

Linux tower with an RTX 4090. Runs Ollama for the [[Librarian]] and the router classifier.
Disk fills up with model weights — see [[2026-10-10 gpu-box disk alert]].
`,
  "products/Leite Mimosa meio-gordo.md": `---
type: product
title: Leite Mimosa meio-gordo
tags: [product, dairy]
store: Continente
size: 1L
usual_qty: 6
---

# Leite Mimosa meio-gordo

1 L, usually 6 per order. Lactose-free variant for [[Ana Pina]].
`,
  "products/Café Delta Lote Chávena.md": `---
type: product
title: Café Delta Lote Chávena
tags: [product, coffee]
store: Continente
---

# Café Delta Lote Chávena

Ground coffee 250 g. Buy 2 when on promotion.
`,
  "products/Pão de forma Bimbo.md": `---
type: product
title: Pão de forma Bimbo
tags: [product, bakery]
---

# Pão de forma Bimbo

Whole-grain, 1 pack per week.
`,
  "episodes/shopping/2026-10-04 Groceries.md": `---
type: episode
title: 2026-10-04 Groceries
tags: [episode, shopping]
agent: grocery-continente
cost_eur: 0.31
spend_eur: 81.12
---

# 2026-10-04 Groceries

[[Grocery (Continente)]] bought 21 items at [[Continente]] for €81.12, delivery Sat 9–11.

- Substituted [[Pão de forma Bimbo]] (out of stock) with the store brand — approved by Daniel.
- [[Leite Mimosa meio-gordo]] ×6, [[Café Delta Lote Chávena]] ×2.

Lesson: check the delivery slot before filling the cart.
`,
  "episodes/2026-10-09 Fix flaky event-stream test.md": `---
type: episode
title: 2026-10-09 Fix flaky event-stream test
tags: [episode, code]
agent: code-fixer
---

# 2026-10-09 Fix flaky event-stream test

[[Code fixer]] reproduced the flake in [[nexus]] on [[mac-studio]] (race between subscribe and
first publish), added a ready signal, ran the suite 50× green. PR merged after approval.
`,
  "episodes/2026-10-10 gpu-box disk alert.md": `---
type: episode
title: 2026-10-10 gpu-box disk alert
tags: [episode, ops, incident]
agent: ops
---

# 2026-10-10 gpu-box disk alert

[[Ops]] found [[gpu-box]] at 91 % disk: 38 GB of unused model weights and Docker layers.
Cleanup proposed; waiting for biometric approval.
`,
  "daily/2026-10-11.md": `---
type: daily
title: 2026-10-11
tags: [daily]
---

# Saturday, 11 October

## Brief
- 3 agents running, 3 things need you.
- Weekly groceries in progress at [[Continente]].
- [[gpu-box]] disk at 91 % — cleanup waiting for you.

## Notes
- Ask [[Rui Costa]] about the Home Assistant integration for [[nexus]].
- [ ] Renew domain for the [[vps]] before Nov 2
`,
};

function titleFrom(path: string, fm: Record<string, YValue>): string {
  const t = asString(fm.title) || asString(fm.name);
  if (t) return t;
  const base = path.split("/").pop() ?? path;
  return base.replace(/\.md$/, "");
}

function build(): MockNote[] {
  const files: Record<string, string> = {};
  for (const [k, v] of Object.entries(templateFiles)) {
    const rel = k.split("/vault-template/")[1];
    if (rel && rel !== "README.md") files[rel] = v;
  }
  if (Object.keys(files).length === 0) {
    // Building outside the monorepo: keep the demo usable with an inline agent.
    files["agents/assistant.md"] = `---
type: agent
id: assistant
name: Assistant
description: General-purpose assistant.
icon: sparkles
runtime: openai
model: smart
tier: medium
tools: [memory.search, vault.read, vault.write, ask_user]
version: 1
---

# Assistant
`;
  }
  Object.assign(files, extra);
  const now = Date.now();
  return Object.entries(files).map(([path, raw], i) => {
    const { frontmatter, body } = splitFrontmatter(raw);
    return {
      id: path.replace(/\.md$/, ""),
      path,
      title: titleFrom(path, frontmatter),
      type: asString(frontmatter.type) || "note",
      tags: asStringList(frontmatter.tags),
      frontmatter,
      body,
      raw,
      updatedAt: now - ((i * 7919) % 14) * DAY - ((i * 104729) % DAY),
    };
  });
}

export const notes: MockNote[] = build();

export function findNote(key: string): MockNote | undefined {
  const k = key.trim().replace(/\.md$/, "").toLowerCase();
  if (!k) return undefined;
  const candidates = notes.filter(
    (n) =>
      n.id.toLowerCase() === k ||
      n.title.toLowerCase() === k ||
      (n.path.split("/").pop() ?? "").replace(/\.md$/, "").toLowerCase() === k,
  );
  // Prefer real notes over templates.
  return candidates.find((n) => !n.path.startsWith("templates/")) ?? candidates[0];
}

export function linksOf(n: MockNote): MockNote[] {
  const out: MockNote[] = [];
  const re = /\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]/g;
  const seen = new Set<string>();
  for (const m of n.raw.matchAll(re)) {
    const t = findNote(m[1]);
    if (t && t.id !== n.id && !seen.has(t.id)) {
      seen.add(t.id);
      out.push(t);
    }
  }
  return out;
}

export function backlinksOf(n: MockNote): MockNote[] {
  return notes.filter((o) => o.id !== n.id && !o.path.startsWith("templates/") && linksOf(o).some((l) => l.id === n.id));
}

export function upsertNote(path: string, raw: string): MockNote {
  const { frontmatter, body } = splitFrontmatter(raw);
  const note: MockNote = {
    id: path.replace(/\.md$/, ""),
    path,
    title: titleFrom(path, frontmatter),
    type: asString(frontmatter.type) || "note",
    tags: asStringList(frontmatter.tags),
    frontmatter,
    body,
    raw,
    updatedAt: Date.now(),
  };
  const i = notes.findIndex((n) => n.path === path);
  if (i >= 0) notes[i] = note;
  else notes.push(note);
  return note;
}
