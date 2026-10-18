#!/usr/bin/env node
// Generates the iOS design tokens from design/tokens.json (the source of truth shared with the
// web dashboard, see web/scripts/gen-tokens.mjs and design/MASTER.md):
//
//   ios/DesignSystem/Generated/DesignTokens.swift   colours (light + OLED dark), fonts with
//                                                   Dynamic Type, spacing, radius, size, motion,
//                                                   elevation
//   ios/App/Assets.xcassets/AccentColor.colorset    the system accent (= primary, both themes)
//
// Usage: node scripts/gen-ios-tokens.mjs            write the files when they changed
//        node scripts/gen-ios-tokens.mjs --check    exit 1 when the committed output is stale
// Wired as `make ios-tokens` (and run by `make gen`).
//
// The fonts named in `font.*.faces` are bundled from ios/DesignSystem/Fonts (static TTF instances
// of Inter, Montserrat and JetBrains Mono, SIL OFL 1.1, licences next to them) and registered with
// UIAppFonts in App/Info.plist and Widgets/Info.plist. This script fails when a face has no file.
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const src = resolve(root, "design/tokens.json");
const swiftOut = resolve(root, "ios/DesignSystem/Generated/DesignTokens.swift");
const accentOut = resolve(root, "ios/App/Assets.xcassets/AccentColor.colorset/Contents.json");
const fontDir = resolve(root, "ios/DesignSystem/Fonts");
const t = JSON.parse(readFileSync(src, "utf8"));
const check = process.argv.includes("--check");

// ---- helpers ------------------------------------------------------------------------------------

/** "surface-2" → "surface2", "border-strong" → "borderStrong", "body-lg" → "bodyLg". */
function ident(key) {
  const s = key.replace(/-([a-z0-9])/g, (_, c) => c.toUpperCase());
  return /^[0-9]/.test(s) ? `s${s}` : s;
}

/** "#RRGGBB" / "#RRGGBBAA" → { hex: "0xRRGGBB", alpha: "1.000" }. */
function parseHex(h) {
  if (!/^#([0-9a-f]{6}|[0-9a-f]{8})$/i.test(h)) throw new Error(`gen-ios-tokens: bad colour ${h}`);
  const rgb = h.slice(1, 7).toUpperCase();
  const a = h.length === 9 ? parseInt(h.slice(7, 9), 16) / 255 : 1;
  return { hex: `0x${rgb}`, alpha: a.toFixed(3), r: parseInt(rgb.slice(0, 2), 16), g: parseInt(rgb.slice(2, 4), 16), b: parseInt(rgb.slice(4, 6), 16), a };
}

function num(v) {
  return Number.isInteger(v) ? `${v}` : `${v}`;
}

/** First layer of a CSS box-shadow → SwiftUI shadow (radius ≈ blur / 2; spread is ignored). */
function parseShadow(css) {
  if (!css || css === "none") return { x: 0, y: 0, radius: 0, r: 0, g: 0, b: 0, a: 0 };
  const first = css.split(/,(?![^()]*\))/)[0].trim();
  const m = first.match(/^(-?[\d.]+)(?:px)?\s+(-?[\d.]+)(?:px)?\s+([\d.]+)(?:px)?(?:\s+(-?[\d.]+)(?:px)?)?\s+rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/);
  if (!m) throw new Error(`gen-ios-tokens: can't parse shadow "${css}"`);
  return { x: +m[1], y: +m[2], radius: +m[3] / 2, r: +m[5], g: +m[6], b: +m[7], a: +m[8] };
}

const textStyle = {
  caption1: ".caption",
  caption2: ".caption2",
  footnote: ".footnote",
  "footnote.monospaced": ".footnote",
  subheadline: ".subheadline",
  body: ".body",
  callout: ".callout",
  headline: ".headline",
  title3: ".title3",
  title2: ".title2",
  title: ".title",
  largeTitle: ".largeTitle",
};

const weights = { 400: "regular", 500: "medium", 600: "semibold", 700: "bold" };

// ---- colours ------------------------------------------------------------------------------------

const light = t.color.light;
const dark = t.color.dark;
const colorKeys = Object.keys(light);
for (const k of colorKeys) if (!(k in dark)) throw new Error(`gen-ios-tokens: colour ${k} has no dark value`);

const uiColors = [];
const colors = [];
for (const k of colorKeys) {
  const l = parseHex(light[k]);
  const d = parseHex(dark[k]);
  uiColors.push(`    /// \`${k}\`: light ${light[k]}, dark ${dark[k]}`);
  uiColors.push(`    static let ${ident(k)} = dynamic(rgb(${l.hex}, ${l.alpha}), rgb(${d.hex}, ${d.alpha}))`);
  colors.push(`    static let ${ident(k)} = Color(uiColor: NXUIColor.${ident(k)})`);
}
const seriesKeys = colorKeys.filter((k) => /^series-\d+$/.test(k));

// ---- fonts --------------------------------------------------------------------------------------

const families = { sans: t.font.sans, heading: t.font.heading, mono: t.font.mono };
const faceFiles = [];
const faceCases = [];
for (const [fam, def] of Object.entries(families)) {
  const faces = def.faces ?? {};
  const avail = Object.keys(faces).map(Number).sort((a, b) => a - b);
  if (!avail.length) throw new Error(`gen-ios-tokens: font.${fam}.faces is empty`);
  for (const w of avail) {
    const ps = faces[w];
    if (!existsSync(resolve(fontDir, `${ps}.ttf`))) throw new Error(`gen-ios-tokens: missing ios/DesignSystem/Fonts/${ps}.ttf`);
    faceFiles.push(ps);
  }
  // Nearest available face for each weight (ties go to the heavier face).
  const lines = [];
  for (const w of [400, 500, 600, 700]) {
    const best = avail.reduce((p, c) => (Math.abs(c - w) < Math.abs(p - w) || (Math.abs(c - w) === Math.abs(p - w) && c > p) ? c : p));
    lines.push(`            case .${weights[w]}: return "${faces[best]}"`);
  }
  faceCases.push(`        case .${fam}:\n            switch weight {\n${lines.join("\n")}\n            }`);
}

const typeLines = [];
for (const [k, v] of Object.entries(t.type)) {
  const fam = v.family ?? (k.startsWith("title") ? "heading" : k === "mono" ? "mono" : "sans");
  const style = textStyle[v.ios];
  if (!style) throw new Error(`gen-ios-tokens: unknown iOS text style ${v.ios} for type.${k}`);
  const weight = weights[v.weight] ?? "regular";
  const spacing = Math.max(0, v.line - v.size);
  const tracking = v.tracking ? +(v.tracking * v.size).toFixed(3) : 0;
  typeLines.push(`    /// \`${k}\`: ${v.size}/${v.line} ${v.weight}, ${families[fam].ios}, scales with \`${v.ios}\`.`);
  typeLines.push(
    `    static let ${ident(k)} = NXTextStyle(family: .${fam}, weight: .${weight}, size: ${num(v.size)}, relativeTo: ${style}, lineSpacing: ${num(spacing)}, tracking: ${num(tracking)})`,
  );
}

// ---- numbers ------------------------------------------------------------------------------------

const space = Object.entries(t.space).map(([k, v]) => `    static let s${k}: CGFloat = ${num(v)}`);
const radius = Object.entries(t.radius).map(([k, v]) => `    static let ${ident(k)}: CGFloat = ${num(v)}`);
const size = Object.entries(t.size).map(([k, v]) => `    static let ${ident(k)}: CGFloat = ${num(v)}`);
const durations = Object.entries(t.motion.duration).map(([k, v]) => `    static let ${ident(k)}: Double = ${(v / 1000).toFixed(3)}`);
const easings = Object.entries(t.motion.easing).map(([k, v]) => {
  const m = v.match(/cubic-bezier\(([^)]+)\)/);
  if (!m) throw new Error(`gen-ios-tokens: bad easing ${v}`);
  const [a, b, c, d] = m[1].split(",").map((x) => x.trim());
  return `    /// \`${k}\`: ${v}\n    static func ${ident(k)}(_ duration: Double) -> Animation {\n        .timingCurve(${a}, ${b}, ${c}, ${d}, duration: duration)\n    }`;
});
const z = Object.entries(t.z).map(([k, v]) => `    static let ${ident(k)}: Double = ${num(v)}`);

const shadowLines = [];
for (const kind of Object.keys(t.shadow.light)) {
  const l = parseShadow(t.shadow.light[kind]);
  const d = parseShadow(t.shadow.dark[kind]);
  const c = (s) => `rgb(0x${[s.r, s.g, s.b].map((x) => x.toString(16).padStart(2, "0").toUpperCase()).join("")}, ${s.a.toFixed(3)})`;
  shadowLines.push(
    `    static let ${ident(kind)} = NXShadowStyle(\n        color: Color(uiColor: NXUIColor.dynamic(NXUIColor.rgb(${c(l).slice(4, -1)}), NXUIColor.rgb(${c(d).slice(4, -1)}))),\n        light: .init(x: ${num(l.x)}, y: ${num(l.y)}, radius: ${num(l.radius)}),\n        dark: .init(x: ${num(d.x)}, y: ${num(d.y)}, radius: ${num(d.radius)})\n    )`,
  );
}

// ---- Swift --------------------------------------------------------------------------------------

const swift = `// GENERATED from design/tokens.json by scripts/gen-ios-tokens.mjs — do not edit.
// Regenerate with \`make ios-tokens\`. Guide: design/MASTER.md.
//
// Compiled into the app and the widget extension (Live Activities, widgets). Colours resolve
// per trait collection, so they follow the system appearance and the in-app Light / System /
// Dark override (\`.preferredColorScheme\`). Fonts scale with Dynamic Type (\`relativeTo:\`).

import SwiftUI
import UIKit

// MARK: - Colours

/// Semantic colours as dynamic \`UIColor\`s (UIKit appearance APIs, tab bars, …).
enum NXUIColor {
${uiColors.join("\n")}

    static func rgb(_ hex: UInt32, _ alpha: CGFloat = 1) -> UIColor {
        UIColor(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: alpha
        )
    }

    static func dynamic(_ light: UIColor, _ dark: UIColor) -> UIColor {
        UIColor { traits in traits.userInterfaceStyle == .dark ? dark : light }
    }
}

/// Semantic colours for SwiftUI. Light = Minimalist SaaS, dark = OLED (MASTER.md §2).
enum NXColor {
${colors.join("\n")}

    /// Categorical colours for charts and agent tiles (\`series-1…${seriesKeys.length}\`).
    static let series: [Color] = [${seriesKeys.map((k) => ident(k)).join(", ")}]
}

// MARK: - Typography

/// A bundled font family (MASTER.md §3): Montserrat for headings and numbers, Inter for body
/// and UI, JetBrains Mono for code, ids and tool output.
enum NXFontFamily: Sendable {
    case sans, heading, mono
}

enum NXFontWeight: Sendable {
    case regular, medium, semibold, bold
}

enum NXFontFace {
    /// PostScript names of the bundled faces (files: ios/DesignSystem/Fonts/<name>.ttf).
    static let all: [String] = [${faceFiles.map((f) => `"${f}"`).join(", ")}]

    /// The bundled face closest to \`weight\` in \`family\`.
    static func name(_ family: NXFontFamily, _ weight: NXFontWeight) -> String {
        switch family {
${faceCases.join("\n")}
        }
    }
}

/// A type role: font, Dynamic Type text style and line spacing (line height − size).
struct NXTextStyle {
    let family: NXFontFamily
    let weight: NXFontWeight
    let size: CGFloat
    let relativeTo: Font.TextStyle
    let lineSpacing: CGFloat
    /// Letter spacing in points at the base size.
    let tracking: CGFloat

    /// The font, scaled with Dynamic Type relative to \`relativeTo\`.
    var font: Font {
        Font.custom(NXFontFace.name(family, weight), size: size, relativeTo: relativeTo)
    }

    /// The same role with another weight (e.g. a semibold body label).
    func weight(_ weight: NXFontWeight) -> NXTextStyle {
        NXTextStyle(family: family, weight: weight, size: size, relativeTo: relativeTo, lineSpacing: lineSpacing, tracking: tracking)
    }
}

/// The type scale (MASTER.md §3).
enum NXType {
${typeLines.join("\n")}
}

// MARK: - Spacing, radius, sizes

/// 4 pt spacing scale (\`space.*\`).
enum NXSpace {
${space.join("\n")}
}

enum NXRadius {
${radius.join("\n")}
}

enum NXSize {
${size.join("\n")}
}

enum NXZIndex {
${z.join("\n")}
}

// MARK: - Motion

/// Durations in seconds and easing curves (MASTER.md §7). Callers honour Reduce Motion.
enum NXMotion {
${durations.join("\n")}

${easings.join("\n\n")}
}

// MARK: - Elevation

struct NXShadowStyle {
    struct Offset {
        let x: CGFloat
        let y: CGFloat
        let radius: CGFloat
    }

    let color: Color
    let light: Offset
    let dark: Offset
}

/// Two levels only (MASTER.md §5): a card hairline shadow and an overlay shadow.
enum NXShadow {
${shadowLines.join("\n")}
}
`;

// ---- AccentColor.colorset ----------------------------------------------------------------------

function comp(c) {
  return { "color-space": "srgb", components: { alpha: c.alpha, blue: (c.b / 255).toFixed(3), green: (c.g / 255).toFixed(3), red: (c.r / 255).toFixed(3) } };
}
const accent = {
  colors: [
    { color: comp(parseHex(light.primary)), idiom: "universal" },
    { appearances: [{ appearance: "luminosity", value: "dark" }], color: comp(parseHex(dark.primary)), idiom: "universal" },
  ],
  info: { author: "gen-ios-tokens", version: 1 },
};
const accentJSON = JSON.stringify(accent, null, 2) + "\n";

// ---- Info.plist UIAppFonts ----------------------------------------------------------------------

for (const plist of ["ios/App/Info.plist", "ios/Widgets/Info.plist"]) {
  const body = readFileSync(resolve(root, plist), "utf8");
  const missing = faceFiles.filter((f) => !body.includes(`<string>${f}.ttf</string>`));
  if (missing.length) throw new Error(`gen-ios-tokens: ${plist} UIAppFonts is missing ${missing.map((f) => f + ".ttf").join(", ")}`);
}

// ---- write --------------------------------------------------------------------------------------

let stale = 0;
for (const [path, body, label] of [
  [swiftOut, swift, "ios/DesignSystem/Generated/DesignTokens.swift"],
  [accentOut, accentJSON, "ios/App/Assets.xcassets/AccentColor.colorset/Contents.json"],
]) {
  const same = existsSync(path) && readFileSync(path, "utf8") === body;
  if (same) continue;
  if (check) {
    console.error(`gen-ios-tokens: ${label} is stale; run make ios-tokens`);
    stale++;
  } else {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, body);
    console.log(`gen-ios-tokens: wrote ${label}`);
  }
}
if (stale) process.exit(1);
