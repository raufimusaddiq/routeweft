# Routeweft UI Style Contract

Status: **Normative for frontend work**

## 1. House style: editorial instrument

Owner decisions #98 (graphite, flat, no decoration) and #102 (direction B,
"editorial") define the style. Routeweft owns its visual language and its static
React + TypeScript + Vite implementation (SPEC section 23, BDR-003/015). PRD
sections 14-15 still govern navigation and operator workflows.

The page reads like an instrument report, not a settings panel:

- **One sentence carries the headline number** at 28px light weight, with its
  basis in a 16px line below (`2,947 requests this week, ↑ 27.8%` /
  `on the week before. 2.7% failed, better than the 30-day 3.4%.`).
- **Figures are captioned.** Each chart or table sits in a `<figure>`; the caption
  (`Fig. 1`, `Table 1`, `Log`) is directly under it at every width.
- **A 300px margin column sits beside every figure** and carries notes that belong
  to it: the week's numbers, a "Needs a look" list, runtime facts. It stacks under
  its figure on tablet and phone.
- **A second statement at headline scale** follows the first figure on each page,
  drawn from the data (the provider failing at a multiple of the fleet rate, the
  largest failure class, traffic concentration). A third statement, the busiest
  hour, is reserved for when the API serves hourly buckets (issue #104).
- **Colour carries state only.** Cyan (`--live`) means data that is current: this
  week's bars, the live card. Red (`--danger`) means risk. Neutral otherwise.
- No gradient, blur, glass, shadow or glow anywhere. Flat fills only.

## 2. Tokens

`ui/src/style.css` is the executable token source.

| Token | Light | Dark |
| --- | --- | --- |
| `--ink`, `--text-main` | `#14161a` | `#ecedef` |
| `--ink-hover` | `#30343a` | `#cdd0d4` |
| `--paper`, `--bg`, `--surface`, `--sidebar` | `#f4f4f2` | `#0c0e11` |
| `--surface-2` | `#e9e9e6` | `#14181d` |
| `--surface-3` | `#d6d6d2` | `#252a31` |
| `--border` (control boundaries) | `#6f757d` | `#6e757e` |
| `--border-subtle` (hairlines) | `#d0d0cc` | `#21262c` |
| `--text-muted`, `--text-subtle` | `#4d5259` | `#9ea4ac` |
| `--danger` | `#b3202f` | `#ff7b86` |
| `--success` | `#11683f` | `#6fd3a0` |
| `--warning` | `#7a5200` | `#e8bb5c` |
| `--live` (current data) | `#00727f` | `#4fd1e0` |
| `--focus` | `#00727f` | `#4fd1e0` |

The accent surface re-points these tokens (`--accent-*`): light uses an ink slab
(`#14161a`, text `#f4f4f2`), dark uses a raised slab (`#151b22`). Control borders
inside it use `--accent-control` (`#6f757d` / `#6e757e`) so they keep 3:1 against
the fill. State chips use state-coloured text and borders with explicit labels.

| Geometry / spacing | Value |
| --- | --- |
| `--radius-flat` | `0`, panels, tables, inputs, selected rows |
| `--radius-control` | `2px`, buttons and small chips only |
| `--rail-width` | `12.5rem` |
| `--control-height` | `28px` desktop |
| `--row-height` | `32px` minimum for single-line rows |
| Section gap above a row | `.gap-s` 40px (same thought), `.gap-m` 72px (new topic), `.gap-l` 120px (new chapter); 56 / 48 and 80 / 64px below 900 / 700px |
| `--motion` / `--hover` | `120ms` entry, `80ms` hover; `cubic-bezier(0.2, 0, 0, 1)` |

Spacing is varied on purpose so sections group. Wrapped rows grow with their
content; fixed heights must not clip labels.

## 3. Typography

- **Space Grotesk** for headings, body and large figures; **JetBrains Mono** for
  tables, technical values, IDs, timestamps and chart text. Both are self-hosted
  from `ui/public/fonts/` as latin variable woff2 with `font-display: swap`
  (licence note beside them). Fallbacks: `ui-sans-serif, system-ui, sans-serif`
  and `ui-monospace, monospace`. Only the Material Symbols stylesheet still loads
  from Google Fonts.
- Scale: **28px** (page title, headline, statement, large figures), **16px**
  (section headings, row names, margin figures), **12px** (body, labels, captions,
  table text), **11px** (chart axis ticks only). Nothing is below 11px. Statements
  drop to 24px below 900px so they stay within two to three lines.
- Labels are sentence case, muted, not uppercase and not letter-spaced.
- Headline and statement: weight 300 with the numbers at 600, tracking `-0.02em`
  (`-0.025em` on the bold numbers); the page title is weight 300, `-0.025em`.
- Tabular numerals everywhere; numbers in tables are right-aligned.
- Running text is `--text-main`; `--text-muted` is for notes and captions and must
  keep 4.5:1.

## 4. Shell and Navigation

Desktop: fixed 12.5rem rail, workspace offset by the rail, content fluid with
56px side padding (28px at 900px, 16px at 700px).

Keep groups, order, labels, icons and generated hashes byte-identical:

| Group | Items, in order |
| --- | --- |
| Operate | Overview; Endpoint & Key; Providers |
| Route | Combo & Capability Adapter; System One |
| Observe | Usage; Quota Tracker; Token Saver |
| System | Console Log; Settings |

Group labels are 12px sentence-case muted text. Active navigation: 2px `--live`
left rule, weight 600 versus inactive 400. No active background pill. Keep
`aria-current`. Navigation icon glyphs are visually hidden in the text-only rail.

At 900px the margin column and split layouts stack under their main column. At
700px the existing drawer replaces the rail; mobile insets are 16px. Buttons,
selects, text inputs and navigation retain 44px minimum targets. Keep
Escape/backdrop dismissal, focus return and all aria attributes. No horizontal
page overflow at 320px or wider.

## 5. Layout components

Implemented in `ui/src/editorial.tsx` and `ui/src/style.css`.

- **Row** (`.ed-row`): main column and margin column; `gap` prop picks 40/72/120px.
- **Main** with an `h2` section heading (16px, 500); **Margin** (`aside`, 300px);
  **Fact** (label 12px muted, value 16px 500, mono note); **Caption** (`Fig. 1`
  kicker in 600, then text); **Statement** (28px, with the action it drives).
- **Two separator weights do different jobs:** a 2px ink rule opens a section,
  margin list or table head; 1px `--border-subtle` rules separate rows.
- **Status is never colour alone:** an 8px square (`.dot`) beside the word. One
  classification serves every mark: HTTP 2xx good, 3xx idle (a hollow square), 4xx warn, 5xx bad.
  A provider's square is red under the same rule as its error figure (below).
  A *failure* is any response that is not a 2xx, the API's own definition of
  `errors` (`usage_summary.go`), so "failed", "Succeeded" and "Largest failure"
  always agree; redirects count in those totals though their mark is neutral.
  The rule lives in `isFailure()` in `ui/src/format.ts`.
- **Accent surface** (`.accent-surface`): exactly one per page, marking the most
  important live thing (Overview: the live card; Usage: the daily chart plate;
  Providers: the provider summary banner; other pages: their toolbar or primary
  panel). Never an empty state. It has a 2px `--live` left rule.
- Panels, tables and inputs have zero radius; buttons and chips 2px.
- Tables collapse borders, use hairline row separators, never zebra stripes, and
  right-align numeric columns.

## 6. Data presentation

- **Daily chart (Fig. 1):** SVG, no chart library. Axis, gridlines, peak label,
  hover and arrow-key readout, and a screen-reader table. The latest 7 days are
  `--live` bars; a 2px tick marks the same day a week earlier; a dashed line is the
  period average; weekends are shaded. Hover shows the change against the week
  before.
- **Provider table (Table 1):** provider with status square, requests, a thin
  share bar, and the error rate with its own bar scaled to 10%. The error bar and
  figure turn `--danger` only when a provider is above 1.5x the fleet rate, above
  3%, with at least 50 requests. At 390px the share bar shrinks to a thin inline
  bar and the error column stays.
- **Request log:** a table on desktop; below 700px each row stacks into route and
  latency on the first line, then status, tokens and time. It never scrolls
  sideways.
- **Outcomes (Fig. 2):** a 100% stacked bar with 2px seams and a labelled legend.
- Every figure carries a unit, a delta or a comparison. A missing value is a dash.
  Sparklines appear only in their own small figure, never on a KPI.

## 7. States

- Loading: skeleton in the shape of the page (lead, figure, rows), no spinner.
- Empty: a ruled box with the reason and the action ("Show all time").
- Error: left rule in the danger colour, the message and a Retry control; stale
  data stays on screen with the error above it. Failure of optional data (usage
  history, request log, connections) never blocks the overview itself.
- Preserve explicit errors, secrets and destructive confirmations.

## 8. Controls and Accessibility

Primary actions: paper-coloured text on an ink fill, dark solid in light mode and
light solid in dark mode. Never blue. Secondary actions use `--border` boundaries;
danger uses its state token. Disabled controls remain visibly disabled. Active
press moves down at most 1px.

Inputs/selects have square corners, readable labels and explicit boundaries. Keep
secrets masked, inline validation and native control semantics. Do not reorder
focusable markup or alter event handlers during visual work.

Every control has a visible 2px focus outline in `--focus`, 2px offset, both
themes (inside the accent surface it switches to `--accent-focus`). Keep
skip-to-content, landmarks, labels, `aria-live`, focus order and reduced motion.
Focus must not be clipped by scroll regions. Normal text and state colours
require WCAG AA 4.5:1; control boundaries and focus require 3:1 against adjacent
surfaces.

## 9. Themes and Motion

Ship light, dark and system. Preserve the pre-paint theme resolver and persisted
preference. System follows OS changes; native controls respect `color-scheme`.

Entry is opacity plus a 2px rise over 120ms on the page content; hover responds
in 80ms. No scale, spring, parallax or continuous decorative animation (the
skeleton pulse is the one exception). Reduced motion removes the rise and keeps
opacity; transitions become effectively immediate.

## 10. Acceptance and Evidence

No API paths, state logic, workflows or nav definitions change for visual work.

1. `npm run typecheck`, `npm test`, `npm run build`, `npm run smoke` pass.
2. `rg -n 'gradient|backdrop-filter|box-shadow' ui/src`: zero matches. Radius
   token values are only 0/2px.
3. `git diff origin/main -- ui/src/main.tsx`: empty.
4. Smoke asserts, on every page: only the sizes 11/12/16/28px (24px for statements
   on a phone), exactly one accent surface, no horizontal overflow, text contrast,
   no computed shell radius above 2px, the 2px focus ring with 2px offset, the
   skeleton, error and empty states, and first-link keyboard focus. Do not weaken
   existing workflow assertions.
5. Overview, Usage and Providers screenshots at 1440px and 390px, light and dark,
   in `docs/evidence/102/final/`. They show a locally seeded database, never
   production traffic.
6. Check contrast, overflow, browser errors, drawer dismissal/focus return,
   persistence, OS-theme changes and reduced motion. Record checks not run.

`ROUTEWEFT_SMOKE_ARTIFACTS` selects the screenshot/report directory (default
`/tmp/routeweft-98-evidence`). Smoke reuses existing control-API fixtures;
Chromium and `playwright-core` are required, as documented in
`ui/scripts/smoke.sh`.
