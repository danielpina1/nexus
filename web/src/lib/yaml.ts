// A deliberately small YAML-ish parser for vault frontmatter: top-level `key: value`, flow
// sequences/maps (`[a, b]`, `{ a: 1, b: [x] }`), one level of block maps / block lists, and folded
// continuation lines. Good enough for fixtures and client-side previews; the Hub owns real parsing.

export type YValue = string | number | boolean | null | YValue[] | { [k: string]: YValue };

export interface ParsedNote {
  frontmatter: Record<string, YValue>;
  body: string;
  hasFrontmatter: boolean;
  error?: string;
}

export function splitFrontmatter(md: string): ParsedNote {
  const text = md.replace(/^﻿/, "");
  if (!text.startsWith("---")) return { frontmatter: {}, body: text, hasFrontmatter: false };
  const end = text.indexOf("\n---", 3);
  if (end < 0) return { frontmatter: {}, body: text, hasFrontmatter: false, error: "Frontmatter is not closed with ---" };
  const raw = text.slice(text.indexOf("\n") + 1, end);
  const rest = text.slice(end + 4).replace(/^\r?\n/, "");
  try {
    return { frontmatter: parseBlock(raw), body: rest, hasFrontmatter: true };
  } catch (e) {
    return { frontmatter: {}, body: rest, hasFrontmatter: true, error: (e as Error).message };
  }
}

function parseBlock(raw: string): Record<string, YValue> {
  const out: Record<string, YValue> = {};
  const lines = raw.split(/\r?\n/);
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim() || line.trim().startsWith("#")) {
      i++;
      continue;
    }
    if (/^\s/.test(line)) throw new Error(`Unexpected indentation on line ${i + 1}: "${line.trim()}"`);
    const m = /^([A-Za-z0-9_.-]+):\s*(.*)$/.exec(line);
    if (!m) throw new Error(`Expected "key: value" on line ${i + 1}: "${line.trim()}"`);
    const key = m[1];
    let rest = m[2];
    i++;
    // Gather indented children.
    const children: string[] = [];
    while (i < lines.length && (lines[i].trim() === "" || /^\s+/.test(lines[i]))) {
      if (lines[i].trim()) children.push(lines[i]);
      i++;
    }
    if (rest === "" || rest === "|" || rest === ">") {
      if (children.length === 0) {
        out[key] = rest === "" ? null : "";
      } else if (children[0].trim().startsWith("- ")) {
        out[key] = parseBlockList(children);
      } else if (rest === "|" || rest === ">") {
        out[key] = children.map((c) => c.trim()).join(rest === "|" ? "\n" : " ");
      } else {
        out[key] = parseBlock(dedent(children));
      }
    } else {
      // Continuation lines of a folded scalar.
      if (children.length && !rest.startsWith("[") && !rest.startsWith("{")) {
        rest = [rest, ...children.map((c) => c.trim())].join(" ");
      } else if (children.length) {
        rest = [rest, ...children.map((c) => c.trim())].join(" ");
      }
      out[key] = parseValue(rest);
    }
  }
  return out;
}

function dedent(lines: string[]): string {
  const ind = Math.min(...lines.map((l) => l.length - l.trimStart().length));
  return lines.map((l) => l.slice(ind)).join("\n");
}

function parseBlockList(lines: string[]): YValue[] {
  const items: YValue[] = [];
  const ind = lines[0].length - lines[0].trimStart().length;
  let cur: string[] | null = null;
  const flush = () => {
    if (!cur) return;
    const first = cur[0];
    if (/^[A-Za-z0-9_.-]+:\s/.test(first) || /^[A-Za-z0-9_.-]+:$/.test(first)) {
      items.push(parseBlock(dedent(cur.map((l, idx) => (idx === 0 ? " ".repeat(ind + 2) + l : l)))));
    } else {
      items.push(parseValue(cur.join(" ")));
    }
    cur = null;
  };
  for (const l of lines) {
    const lind = l.length - l.trimStart().length;
    if (lind === ind && l.trim().startsWith("- ")) {
      flush();
      cur = [l.trim().slice(2)];
    } else if (cur) {
      cur.push(l);
    }
  }
  flush();
  return items;
}

export function parseValue(s: string): YValue {
  const p = new FlowParser(s.trim());
  const v = p.value();
  return v;
}

class FlowParser {
  i = 0;
  constructor(private s: string) {}
  ws() {
    while (this.i < this.s.length && /\s/.test(this.s[this.i])) this.i++;
  }
  value(): YValue {
    this.ws();
    const c = this.s[this.i];
    if (c === "[") return this.seq();
    if (c === "{") return this.map();
    if (c === '"' || c === "'") return this.quoted();
    return scalar(this.plain());
  }
  seq(): YValue[] {
    this.i++;
    const out: YValue[] = [];
    this.ws();
    if (this.s[this.i] === "]") {
      this.i++;
      return out;
    }
    for (;;) {
      out.push(this.value());
      this.ws();
      const c = this.s[this.i++];
      if (c === "]") return out;
      if (c !== ",") throw new Error(`Expected "," or "]" in "${this.s}"`);
    }
  }
  map(): Record<string, YValue> {
    this.i++;
    const out: Record<string, YValue> = {};
    this.ws();
    if (this.s[this.i] === "}") {
      this.i++;
      return out;
    }
    for (;;) {
      this.ws();
      const k = this.s[this.i] === '"' || this.s[this.i] === "'" ? String(this.quoted()) : this.key();
      this.ws();
      if (this.s[this.i] !== ":") throw new Error(`Expected ":" after "${k}"`);
      this.i++;
      out[k] = this.value();
      this.ws();
      const c = this.s[this.i++];
      if (c === "}") return out;
      if (c !== ",") throw new Error(`Expected "," or "}" in "${this.s}"`);
    }
  }
  key(): string {
    const start = this.i;
    while (this.i < this.s.length && !/[:,{}[\]]/.test(this.s[this.i])) this.i++;
    return this.s.slice(start, this.i).trim();
  }
  quoted(): string {
    const q = this.s[this.i++];
    const start = this.i;
    while (this.i < this.s.length && this.s[this.i] !== q) this.i++;
    const v = this.s.slice(start, this.i);
    this.i++;
    return v;
  }
  plain(): string {
    const start = this.i;
    let depthAware = this.s.slice(this.i);
    // Top-level plain scalars run to the end; inside flow collections stop at , ] }
    const inFlow = this.s[0] === "[" || this.s[0] === "{";
    if (!inFlow) {
      this.i = this.s.length;
      return depthAware.trim();
    }
    while (this.i < this.s.length && !/[,\]}]/.test(this.s[this.i])) this.i++;
    depthAware = this.s.slice(start, this.i);
    return depthAware.trim();
  }
}

function scalar(s: string): YValue {
  if (s === "" || s === "~" || s === "null") return s === "" ? "" : null;
  if (s === "true") return true;
  if (s === "false") return false;
  if (/^-?\d+(\.\d+)?$/.test(s)) return Number(s);
  return s;
}

export function asString(v: YValue | undefined): string {
  if (v == null) return "";
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  return JSON.stringify(v);
}

export function asStringList(v: YValue | undefined): string[] {
  if (Array.isArray(v)) return v.map(asString);
  if (v == null || v === "") return [];
  return [asString(v)];
}

/** "30m" | "1h" | "90s" | 600 → seconds */
export function parseDurationSeconds(v: YValue | undefined): number {
  if (typeof v === "number") return v;
  const m = /^(\d+(?:\.\d+)?)\s*(s|m|h)?$/.exec(asString(v));
  if (!m) return 0;
  const n = Number(m[1]);
  return Math.round(m[2] === "h" ? n * 3600 : m[2] === "m" ? n * 60 : n);
}
