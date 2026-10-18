# Nexus design system: index

| File | What it is |
|---|---|
| [`MASTER.md`](MASTER.md) | **The guide.** Principles, both theme palettes with measured contrast, type, spacing, radius, elevation and glass, motion, icons, component state rules, accessibility, the iOS (SwiftUI) mapping, and the deviations from UI UX Pro Max. |
| [`tokens.json`](tokens.json) | **The values.** Colours (light and dark), fonts, type scale, spacing, radii, shadows, durations, sizes and z-index. Web: `cd web && npm run tokens` generates `web/src/styles/tokens.css` and checks contrast; it also runs before dev, build and typecheck. iOS: generate Swift constants and an asset catalog from the same file. |
| [`../design-system/nexus/MASTER.md`](../design-system/nexus/MASTER.md) | The raw output of the UI UX Pro Max skill (`python3 .claude/skills/ui-ux-pro-max/scripts/search.py "AI agent personal operating system dashboard productivity real-time" --design-system -p Nexus --density 7 --variance 3 --motion 3 --persist`), with page notes for dashboard, inbox and chat. For reference only. |
| `ios-sketch/` | Early iOS sketches. |

To change a colour, edit `tokens.json`, run `npm run tokens -- --report` in `web/` (it fails on any contrast pair
below AA), and update the table in `MASTER.md`.
