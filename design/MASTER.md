# Nexus Design System: Master Guide

> The single guide for the Nexus web dashboard and, later, the iOS app.
> **Tokens:** [`design/tokens.json`](tokens.json) is the source of truth for every value below.
> The web app generates CSS variables from it (`web/scripts/gen-tokens.mjs` → `web/src/styles/tokens.css`,
> which runs before `dev`, `build` and `typecheck` and fails the build on a contrast regression). The iOS app should
> generate Swift constants from the same file.
> **Basis:** the UI UX Pro Max skill (`.claude/skills/ui-ux-pro-max`). Its generated output lives in
> [`design-system/nexus/MASTER.md`](../design-system/nexus/MASTER.md), with page notes in `design-system/nexus/pages/`.
> This file is the curated version; where they differ, this file wins. The deviations are listed in §12.

---

## 1. Principles

1. **Calm by default.** Nothing moves, glows or turns red unless it needs you. Tint only what is waiting on you
   (pending approvals, unanswered questions).
2. **One input for everything.** The command bar and ⌘K palette run commands, pick agents (`@agent`), search
   notes and jump anywhere. Every feature is reachable from the keyboard.
3. **Real time, no spinners.** State arrives over one event stream. Actions are optimistic: they show the result at
   once and roll back with an error toast if the Hub refuses.
4. **Quality over quantity.** A screen ships polished or not at all. Every list has loading, empty and error states.
5. **Same language on every surface.** Web, iOS, notifications and widgets use the same cards, states and words.

**Style:** *Minimalism & Swiss* (UI UX Pro Max style for dashboards). Light theme is **Minimalist SaaS**; dark theme
is **Dark Mode (OLED)**. The layout is grid based with clear type hierarchy, flat surfaces with hairline borders, and
a single accent colour. Subtle glass is used only on chrome. No gradients, no 3D and no emoji icons.

## 2. Themes and palette

The app follows the system preference (`prefers-color-scheme`) by default. A **Light / System / Dark** control in
the sidebar footer (or the More sheet on phones, or Settings → Appearance) overrides it. The choice is stored in
`localStorage` (`nexus.settings.theme`, read and written inside try/catch) and applied before first paint, so the
page never flashes the wrong theme.

| Token | Light (Minimalist SaaS) | Dark (OLED) | Use |
|---|---|---|---|
| `bg` | `#F8FAFC` | `#0A0A0B` | App background |
| `surface` | `#FFFFFF` | `#111113` | Cards, inputs |
| `surface-2` | `#F1F5FD` | `#18181B` | Hover, code, inset areas |
| `surface-3` | `#E2E8F0` | `#232327` | Selected nav, tracks |
| `border` | `#E4ECFC` | `#222226` | Decorative hairlines (cards, dividers) |
| `border-strong` | `#CBD5E1` | `#3F3F46` | Emphasis hairlines |
| `border-input` | `#7C8BA6` | `#6E6E78` | **Control boundaries** (inputs, selects, command bar) |
| `fg` | `#0F172A` | `#F4F4F5` | Primary text |
| `fg-2` | `#334155` | `#D4D4D8` | Secondary text |
| `muted` | `#475569` | `#A1A1AA` | Meta text, placeholder |
| `primary` / `primary-fg` | `#2563EB` / `#FFFFFF` | `#60A5FA` / `#0A0A0B` | Primary actions, user bubbles |
| `accent` / `accent-soft` | `#2563EB` / `#EEF3FE` | `#60A5FA` / `#0F1D33` | Links, active nav icon, selection |
| `ring` | `#2563EB` | `#60A5FA` | Focus ring |
| `info` / `info-soft` | `#0E7490` / `#E8F7FA` | `#22D3EE` / `#0A2329` | Running / live state |
| `ok` / `ok-soft` | `#15803D` / `#EEF8F1` | `#4ADE80` / `#0B2316` | Succeeded, approve |
| `warn` / `warn-soft` | `#B45309` / `#FDF3E3` | `#FBBF24` / `#241B07` | Waiting on you, approvals |
| `danger` / `danger-soft` | `#C81E1E` / `#FDF0F0` | `#F87171` / `#2A1112` | Failed, deny, destructive |
| `overlay` | `#0F172A` 50 % | `#000000` 70 % | Modal scrim |
| `glass` / `glass-border` | `#FFFFFF` 72 % / `#E4ECFC` | `#111113` 72 % / white 8 % | Chrome (see §6) |
| `series-1…8` | categorical | categorical | Charts, agent colours |

### Contrast (measured by `npm run tokens -- --report`; the build fails below the minimum)

| Pair | Light | Dark | Minimum |
|---|---|---|---|
| fg on bg | 17.06 | 18.00 | 4.5 |
| fg-2 on surface | 10.35 | 12.76 | 4.5 |
| muted on surface / bg / surface-2 | 7.58 / 7.24 / 6.94 | 7.36 / 7.72 / 6.91 | 4.5 |
| primary-fg on primary (buttons) | 5.17 | 7.78 | 4.5 |
| accent on surface (links) | 5.17 | 7.42 | 4.5 |
| ok / warn / danger / info on surface | 5.02 / 5.02 / 5.74 / 5.36 | 10.82 / 11.30 / 6.82 / 10.44 | 4.5 |
| **badges:** ok / warn / danger / info / accent on their `-soft` tint | 4.62 / 4.57 / 5.16 / 4.88 / 4.65 | 9.52 / 10.19 / 6.39 / 9.04 / 6.64 | 4.5 |
| border-input on surface / bg (non-text) | 3.44 / 3.29 | 3.74 / 3.92 | 3.0 |
| focus ring on bg / surface (non-text) | 4.94 / 5.17 | 7.78 / 7.42 | 3.0 |

Filled buttons in dark mode use dark text (`text-bg`) on the light tone: white on `#F87171` would be only 2.8:1.
Card hairlines (`border`) are decorative and are not needed to identify a control. Anything you type into or
operate uses `border-input`.

## 3. Typography

**Pairing:** **Montserrat** for headings (page titles, section headers, stat numbers) and **Inter** for body,
tables and UI, with **JetBrains Mono** for code, streamed tool output and ids. All three are self-hosted
(`@fontsource-variable/*`), so they work offline from the Hub with no third-party requests. Figures use
tabular numbers (`.tabular`). The headings rule is global: `h1, h2, h3, .font-heading`.

| Role | Size / line | Weight | Font | iOS Dynamic Type |
|---|---|---|---|---|
| title-1 (page title) | 24/30 (22 on inner pages) | 600, −0.01em | Montserrat | `.title2` |
| title-2 (stat number) | 20–22/26–28 | 600, tabular | Montserrat | `.title3` |
| title-3 (section header) | 15–16/22 | 600 | Montserrat | `.headline` |
| body-lg (inputs on touch) | 16/24 | 400 | Inter | `.body` |
| body | 14/21 (13.5 in dense lists) | 400 | Inter | `.subheadline` |
| footnote | 13/18 | 400 | Inter | `.footnote` |
| caption (meta, badges) | 11–12/16 | 500 | Inter | `.caption1` |
| mono | 12–12.5/19 | 400 | JetBrains Mono | `.footnote.monospaced()` |

Inputs are 16 px on phones so iOS Safari doesn't zoom, and 13 px from `sm` up.

## 4. Spacing, layout and radius

- **Spacing:** a 4 px base with tokens `space.1…12` (4, 8, 12, 16, 20, 24, 32, 40, 48). Section rhythm: 32 between
  page sections, 20–24 inside cards and 12 between list items.
- **Layout:** the sidebar is 232 px (desktop, `lg` ≥ 1024). Content is at most 1200 px wide, with gutters of 16 (phone),
  24 (tablet) and 40 (desktop). Phones get a 48 px top bar and a 56 px bottom tab bar, both safe-area aware; scroll
  content gets bottom padding so nothing hides behind the bar.
- **Radius:** `sm` 6 (badges, kbd), `md` 8 (buttons, inputs), `lg` 12 (cards), `xl` 16 (dialogs, sheets, command
  bar), `full` (pills, avatars).
- **Z-index scale:** sticky 10, nav 20, sheet 30, dialog 50, toast 60.

## 5. Elevation

The style is flat. Cards are separated by a hairline border plus `shadow.card` (light: 1 px with 4 % opacity; dark:
none). Only overlays (palette, dialogs, toasts, menus, the "Latest" pill) use `shadow.overlay`. Hover never lifts or
moves a card; only its border colour changes, so layout never shifts.

## 6. Glass (chrome only)

`.glass` means a translucent `glass` fill, a `blur(16px) saturate(160%)` backdrop filter and the `glass-border`
hairline. It is used **only** on the sidebar, the phone top bar and bottom tab bar, the More sheet, the command
palette, dialogs and toasts. It is never used on content cards. It falls back to solid `surface` under
`prefers-reduced-transparency: reduce`. Text on glass is always `fg` or `fg-2`, measured against the solid fallback.

## 7. Motion

| Token | ms | Use |
|---|---|---|
| instant | 100 | Press feedback (`active:scale-[0.98]`, opacity) |
| fast | 150 | Hover colour, tab and nav changes |
| base | 200 | Page fade-up (4 px), dialog pop-in, toast in |
| slow | 250 | Sheet slide-up |

The easing is `out` = `cubic-bezier(0.22, 1, 0.36, 1)`. At most one or two elements animate per view. Live dots
pulse slowly (2.4 s); the streaming caret blinks. Under `prefers-reduced-motion: reduce` every animation and
transition is cut to 1 ms with a single iteration, and smooth scrolling is turned off, so the final state shows at once.

## 8. Iconography

The icon set is **Lucide**: outline icons with a 1.75–2 px stroke (1.9 in navigation, 2.1 when active). Sizes are
14 (inline), 16 (buttons), 17–18 (nav) and 22 (tab bar). Icons beside text are decorative (`aria-hidden`). Icon-only
controls always have an `aria-label`, and a `title` where hover helps. Agents get a tinted tile in their categorical
series colour. No emoji are used as icons.

## 9. Components and state rules

The shared states are:

- **default**
- **hover:** surface-2 fill or a stronger border, 150 ms.
- **focus-visible:** a 2 px `ring` outline with a 2 px offset, on every interactive element, from the global rule.
- **active:** press scale 0.98 or opacity 0.9.
- **disabled:** 50–60 % opacity, `cursor-not-allowed`, native `disabled`, no tap action.
- **loading:** a spinner replaces the icon, the control is disabled, and its width stays the same.
- **error:** `danger` text or border, a message next to the field linked by `aria-describedby`, and `role="alert"`.
- **empty:** an `EmptyState` with an icon tile, a one-line title, one sentence and an optional action.

| Component | Default | Hover | Focus-visible | Active | Disabled | Loading | Error | Empty |
|---|---|---|---|---|---|---|---|---|
| **Button: primary** | `primary` fill, `primary-fg` text, h 32/36 (44 on touch) | opacity .9 | ring | scale .98 | 50 % | spinner, disabled | toast with Retry | n/a |
| **Button: secondary** | surface, hairline, card shadow | surface-2 | ring | scale .98 | 50 % | spinner | toast | n/a |
| **Button: ghost / outline / danger / success** | text only / border-strong / danger fill / ok fill | surface-2 / surface-2 / .9 / .9 | ring | scale .98 | 50 % | spinner | toast | n/a |
| **Input / select** | surface, `border-input`, radius md, h 36 (44 touch) | border fg-2/60 | ring + border ring | n/a | 60 %, not-allowed | n/a | `aria-invalid` → danger border, message below with `role=alert` | placeholder in `muted` |
| **Card** | surface, hairline, card shadow, radius lg | linked cards: border-strong | ring (when a link) | n/a | n/a | `Skeleton` blocks of the same size | `ErrorState` (icon, message, Retry) | `EmptyState` |
| **Badge / status pill** | `-soft` tint plus tone text, h 20, radius sm, icon plus label (never colour alone) | n/a | n/a | n/a | n/a | spinner icon for running | danger tone | n/a |
| **List row** (tasks, notes, files) | ≥ 48 px (60 for tasks), divider between rows | surface-2 | ring | n/a | n/a | `SkeletonRows` | `ErrorState` | `EmptyState`; lists over 80 rows are virtualized |
| **Approval card** | surface. **Pending:** warn/40 border (biometric: danger/40), badge, expiry countdown (danger under 10 min), Approve (success) and Deny (secondary) | n/a | ring on buttons | n/a | biometric: "Approve on iPhone" (the web can't sign) | n/a (optimistic: the card leaves the inbox at once) | rollback; the card returns with an error toast and Retry | inbox: "All clear" |
| **Question card** | info/35 border while unanswered, choice buttons and a free-text field | n/a | ring | n/a | answered → "answered" ok badge | optimistic: marked answered at once | rollback plus toast | n/a |
| **Run / stream view** | prompt bubble (primary), streamed Markdown, collapsible tool rows (mono), state dividers, inline approvals and questions, cost meter, reply box | tool row: surface-2 | ring | n/a | n/a | "Finding the right machine…" with a spinner, and a blinking caret while streaming | inline danger message; failed replies show "Not sent" | "No output recorded." |
| **Command palette** | glass dialog, 56 px input, grouped results (Top hit · Run · Needs you · Go to · Tasks · Agents · Knowledge · Actions) | row: surface-2 | input: 2 px ring underline; rows: `aria-selected` | Enter runs | n/a | spinner icon while searching notes | n/a | "No matches. Press Enter to ask Nexus." |
| **Tab bar (phone)** | glass, 56 px plus safe area, 5 items (Home, Inbox, Chat, Work, More), icon 22 plus label 10.5 | n/a | ring | active: accent icon, fg label | n/a | n/a | n/a | n/a |
| **Sheet (More)** | glass, radius xl top, grab handle, 48 px rows | surface-2 | ring | n/a | n/a | n/a | n/a | n/a |
| **Toast** | glass, bottom-right (above the tab bar on phones), tone icon, optional action ("Retry") | n/a | ring | n/a | n/a | n/a | error toasts last 8 s | n/a |
| **Section tabs** | underline tabs, h 40 (44 touch) | text fg | ring | 2 px accent underline | n/a | n/a | n/a | n/a |
| **Segmented tabs** | Radix Tabs with arrow-key roving focus | surface-2 | ring | surface-3 fill | n/a | n/a | n/a | n/a |
| **Connection indicator** | dot plus label: Live (ok, slow pulse), Connecting / Reconnecting in Ns (warn), Offline (danger), Token rejected (danger) | clickable when not live ("retry now") | ring | n/a | n/a | n/a | the offline banner offers "Retry now" and a link to settings | n/a |

**Destructive actions** (kill switch, cancelling a run) need a second, explicit confirmation. Cancel becomes
"Confirm cancel" for 4 s. Approving is never one tap away from the wrong card: the buttons sit inside the card they act on.

## 10. Accessibility rules (WCAG 2.2 AA)

- Contrast: at least 4.5:1 for text and 3:1 for large text, icons, control borders and the focus ring, in **both**
  themes. `gen-tokens` enforces this; see §2.
- Focus: the global `:focus-visible` ring is a 2 px primary outline with a 2 px offset. Nothing removes it unless it
  supplies an equal indicator (the command bar and palette show a ring on their container).
- Keyboard: ⌘K / Ctrl K opens the palette, `/` focuses the command bar, `g` plus a key jumps to a page, `?` shows
  the shortcuts and `` ` `` toggles the perf HUD. Dialogs trap focus and give it back on close (Radix). Tabs use the
  arrow keys. A skip link goes to the content.
- Touch: targets are at least 44×44 on coarse pointers (`pointer-coarse:` sizes on buttons, tabs, rows and inputs) and
  at least 24×24 on desktop.
- Semantics: every icon-only button has an `aria-label`; badge counts are announced ("3 need you"); toasts are in
  a `role=status` live region; errors use `role=alert`; status is never shown by colour alone.
- Motion and transparency: see §6 and §7. Text stays readable at 200 % zoom, and no screen scrolls sideways at 320 px.

## 11. Mapping to iOS (SwiftUI)

| Web | iOS |
|---|---|
| Inter body / Montserrat headings | **SF Pro Text / SF Pro Display** (system). Keep Montserrat only for large titles if brand consistency matters; prefer the system font for Dynamic Type. |
| JetBrains Mono | `Font.system(.footnote, design: .monospaced)` (SF Mono) |
| Type scale (§3) | Dynamic Type styles in the last column of §3; never fixed sizes |
| `tokens.json` colours | An asset catalog with Any/Dark appearances generated from `color.light` and `color.dark` (`Color("surface")`, …) |
| `.glass` | `.background(.ultraThinMaterial)` / `.bar` for the tab bar and toolbars; `.regularMaterial` for sheets |
| `overlay` scrim | system sheet and alert presentation |
| Radius lg 12 / xl 16 | `RoundedRectangle(cornerRadius: 12, style: .continuous)` |
| Motion tokens | `.animation(.snappy(duration: 0.2))`; respect `accessibilityReduceMotion` |
| Touch 44 | `.frame(minWidth: 44, minHeight: 44)` / `.contentShape(Rectangle())` |
| Phone tab bar (Home, Inbox, Chat, Work, More) | `TabView` with the same five tabs; `NavigationStack` per tab (`navigationDestination(for:)`); `NavigationSplitView` on iPad |
| Command palette | `.searchable` plus a "Run" suggestion; Spotlight and App Intents for the same commands |
| Approval card | the same layout; biometric approvals sign with Face ID (Secure Enclave) |
| Lucide icons | SF Symbols: house (Home), tray (Inbox), bubble.left (Chat), square.stack (Work), books.vertical (Knowledge), cpu (Machines), gearshape (Settings), checkmark.circle (succeeded), xmark.circle (failed), hand.raised (waiting approval), questionmark.bubble (waiting input), faceid (biometric), arrow.up.circle (try harder), stop.circle (cancel) |

## 12. Deviations from the generated UI UX Pro Max output

| Generated (`design-system/nexus/MASTER.md`) | Here | Why |
|---|---|---|
| Category "Translator App" | Productivity / AI agent dashboard | A misclassification by the search engine; its style, palette and font picks still fit a dashboard |
| Primary CTA buttons in accent orange `#EA580C` with black text | Blue primary buttons; no orange | Orange collides with the amber "waiting on you" warning semantics; one accent colour (Swiss) |
| Secondary `#0891B2` | `#0E7490` light / `#22D3EE` dark, as `info` (running state) | `#0891B2` is 3.7:1 on white, which fails AA for text |
| Muted fg `#475569` | Adopted for light | n/a |
| Card hover lift `translateY(-2px)` plus `shadow-md→lg` | Border colour change only, no movement | Flat style, no layout shift (pro-rules: stable interaction states) |
| Shadow scale sm…xl | Two levels: card hairline and overlay | "Avoid complex shadows" from the same output |
| Dark palette: none generated | OLED dark derived from the same hues | Dark is a first-class theme for an agent console |
| Fonts: Inter only (Google Fonts) | Inter body plus Montserrat headings, self-hosted | The user's spec; self-hosting works offline from the Hub |
| Landing-page "pattern" | Ignored | This is an app, not a landing page |
