#!/usr/bin/env node
// Generates web/src/styles/tokens.css from design/tokens.json (the single source of truth shared
// with the iOS app). Runs before dev/build/typecheck; `--check` fails if the CSS is stale.
// It also verifies WCAG contrast for the text/background pairs the UI relies on.
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const src = resolve(here, "../../design/tokens.json");
const out = resolve(here, "../src/styles/tokens.css");
const t = JSON.parse(readFileSync(src, "utf8"));

const px = (v) => (typeof v === "number" ? `${v}px` : v);
const ms = (v) => `${v}ms`;
function hexA(h) {
  // #RRGGBBAA → rgb(r g b / a)
  if (/^#[0-9a-f]{8}$/i.test(h)) {
    const n = (i) => parseInt(h.slice(i, i + 2), 16);
    return `rgb(${n(1)} ${n(3)} ${n(5)} / ${(n(7) / 255).toFixed(2)})`;
  }
  return h;
}
function vars(mode) {
  const lines = [];
  for (const [k, v] of Object.entries(t.color[mode])) lines.push(`  --${k}: ${hexA(v)};`);
  lines.push(`  --shadow: ${t.shadow[mode].card === "none" ? "0 0 0 transparent" : t.shadow[mode].card};`);
  lines.push(`  --shadow-lg: ${t.shadow[mode].overlay};`);
  return lines.join("\n");
}
const shared = [];
shared.push(`  --font-sans-stack: ${t.font.sans.web};`);
shared.push(`  --font-mono-stack: ${t.font.mono.web};`);
shared.push(`  --font-heading-stack: ${t.font.heading.web};`);
for (const [k, v] of Object.entries(t.radius)) shared.push(`  --radius-${k}: ${px(v)};`);
for (const [k, v] of Object.entries(t.motion.duration)) shared.push(`  --dur-${k}: ${ms(v)};`);
for (const [k, v] of Object.entries(t.motion.easing)) shared.push(`  --ease-${k}: ${v};`);
for (const [k, v] of Object.entries(t.size)) shared.push(`  --size-${k}: ${px(v)};`);
for (const [k, v] of Object.entries(t.type)) {
  shared.push(`  --text-${k}-size: ${px(v.size)};`);
  shared.push(`  --text-${k}-line: ${px(v.line)};`);
}

const css = `/* GENERATED from design/tokens.json by web/scripts/gen-tokens.mjs — do not edit. */
:root {
  color-scheme: light;
${vars("light")}
${shared.join("\n")}
}

.dark {
  color-scheme: dark;
${vars("dark")}
}
`;

// ---- contrast check ----
function lum(hex) {
  hex = hex.slice(0, 7);
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}
function ratio(a, b) {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}
const pairs = [
  ["fg", "bg", 4.5], ["fg-2", "surface", 4.5], ["muted", "surface", 4.5], ["muted", "bg", 4.5],
  ["primary-fg", "primary", 4.5], ["accent", "surface", 4.5], ["ok", "surface", 3], ["warn", "surface", 3],
  ["danger", "surface", 3], ["info", "surface", 3], ["ok", "ok-soft", 4.5], ["warn", "warn-soft", 4.5],
  ["danger", "danger-soft", 4.5], ["info", "info-soft", 4.5], ["accent", "accent-soft", 4.5],
  // Non-text contrast (WCAG 1.4.11): control boundaries and the focus ring.
  ["border-input", "surface", 3], ["border-input", "bg", 3], ["ring", "bg", 3], ["ring", "surface", 3],
  ["muted", "surface-2", 4.5], ["fg-2", "surface-2", 4.5],
];
let bad = 0;
const report = [];
for (const mode of ["light", "dark"]) {
  const c = t.color[mode];
  for (const [f, b, min] of pairs) {
    const r = ratio(c[f], c[b]);
    report.push(`${mode.padEnd(5)} ${f.padEnd(10)} on ${b.padEnd(11)} ${r.toFixed(2)}:1${r < min ? `  FAIL (< ${min})` : ""}`);
    if (r < min) bad++;
  }
}
if (process.argv.includes("--report")) console.log(report.join("\n"));
if (bad) {
  console.error(report.filter((l) => l.includes("FAIL")).join("\n"));
  console.error(`gen-tokens: ${bad} contrast failure(s)`);
  process.exit(1);
}

if (process.argv.includes("--check")) {
  if (!existsSync(out) || readFileSync(out, "utf8") !== css) {
    console.error("gen-tokens: src/styles/tokens.css is stale; run npm run tokens");
    process.exit(1);
  }
} else if (!existsSync(out) || readFileSync(out, "utf8") !== css) {
  writeFileSync(out, css);
  console.log("gen-tokens: wrote src/styles/tokens.css");
}
