# Routeweft UI Style Contract

Status: **Normative for frontend work**

> **Transition (#263, BDR-027).** The visual language below replaces the BDR-015
> graphite rule. The owner chooses the direction from the phase 1 previews
> (`ui/preview/263/`, screenshots in `docs/evidence/263/preview/`): A "Loom",
> B "Patchwork", C "Ledger" or D "Signal" (one-line trade-offs in BDR-027). The
> recommendation is C "Ledger". Token values in §2 are A's, kept as a
> placeholder until the owner picks; §2 then follows that choice before phase 2. Until phase 2 lands, `ui/src/style.css` still holds
> the BDR-015 values (git `f15b350`) and the smoke guards in §10 enforce them;
> code, guards and this file then change together, phase by phase.

## 1. House style: editorial instrument, retro neo-brutalist

Owner decision #102 (direction B, "editorial") defines the layout; owner decision
#263 (BDR-027, superseding the visual part of #98) defines the surface: tinted
paper, ink outlines where a box means something, a hard offset shadow with no
blur, a few meaningful tints and one highlighter. Routeweft owns its visual language and its static
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
- **Colour carries meaning, never decoration.** Cyan (`--live`) means data that is
  current: this week's bars, the live card. Red (`--danger`) means risk. The lime
  highlighter (`--highlight`) marks the one number a sentence is about and the
  active place in the rail. Each tint means one thing (§2). Neutral otherwise.
- **A box is drawn only when it means something:** the accent surface, notices,
  empty and error states, and controls get a 2px ink outline; figures, tables and
  margin lists stay open on the page with rules (§5).
- **Depth is a hard offset shadow, never a blur.** `--hard` (4px 4px 0) on the
  accent surface, `--hard-sm` (2px 2px 0) on primary buttons and notices. No
  gradient, blur, glass, glow or backdrop filter anywhere. Flat fills only.

## 2. Tokens

`ui/src/style.css` is the executable token source. Light and dark set the same
names. Contrast ratios are WCAG 2 against the paper unless noted.

| Token | Light | Dark |
| --- | --- | --- |
| `--ink`, `--text-main` | `#17181a` (14.9:1) | `#eeede6` (16.0:1) |
| `--ink-hover` | `#34362f` | `#cfcec6` |
| `--paper`, `--bg` | `#ecebe4` linen | `#111210` |
| `--surface` (inside a box) | `#f8f7f2` | `#1a1b18` |
| `--sidebar` | `#e3e1d7` | `#151613` |
| `--surface-2` | `#dedcd2` | `#23251f` |
| `--surface-3` | `#c9c6ba` | `#34372f` |
| `--border` (control boundaries) | `#6d6a60` (4.5:1) | `#85877c` (5.2:1) |
| `--border-subtle` (hairlines) | `#c9c6ba` | `#2c2e28` |
| `--outline` (box outline) | `#17181a` | `#eeede6` |
| `--shadow-ink` | `#17181a` | `#5d6055` |
| `--text-muted`, `--text-subtle` | `#4f4d46` (7.1:1) | `#a9aa9e` (8.0:1) |
| `--danger` | `#a8202c` (6.0:1) | `#ff7b86` (7.5:1) |
| `--success` | `#1d6b3c` (5.5:1) | `#6fd3a0` (10.3:1) |
| `--warning` | `#7a5200` (5.8:1) | `#e8bb5c` (10.5:1) |
| `--live`, `--focus` | `#00727f` (4.7:1) | `#4fd1e0` (10.3:1) |
| `--highlight` (marker under text) | `#d8f25a` | `#4f5c14` (light text on it 6.2:1) |
| `--highlight-solid` / `--highlight-solid-ink` (filled block) | `#d8f25a` / `#17181a` (14.2:1) | same |
| `--tint-live` (the accent surface) | `#cbe8e6` | `#10353a` |
| `--tint-warn` | `#f1e2b0` | `#3d3112` |
| `--tint-danger` (error state) | `#f6d3cc` (ink 12.8:1) | `#47191d` (ink 12.6:1) |
| `--hard` / `--hard-sm` | `4px 4px 0 var(--shadow-ink)` / `2px 2px 0 var(--shadow-ink)` | same |

The accent surface re-points these tokens (`--accent-*`) onto `--tint-live`:
light text `#17181a` (13.7:1), muted `#3d4744` (7.4:1), control boundary
`#4f6461` (4.9:1), live and focus `#005a64` (6.1:1), danger `#8f1a24` (6.9:1);
dark text `#eeede6` (11.2:1), muted `#a9c4c1` (7.1:1), control `#6f9a96` (4.2:1),
live and focus `#6fe0ee` (8.5:1), danger `#ff8a93` (5.9:1). State chips use
state-coloured text with an ink outline and explicit labels.

| Geometry / spacing | Value |
| --- | --- |
| `--radius-flat` | `0`, boxes, panels, tables, inputs, selected rows |
| `--radius-control` | `2px`, buttons and small chips only |
| Outline | `2px solid var(--outline)` on a box that means something and on buttons; inputs `2px solid var(--border)`; 1px `--border-subtle` hairlines between rows |
| Shadow | `--hard` on the accent surface; `--hard-sm` on primary buttons, notices, empty and error states; nothing else |
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
  (`-0.025em` on the bold numbers); the bold numbers carry the `--highlight`
  marker, a 0.42em underline drawn behind the glyphs (`text-decoration`, not a
  gradient). The page title is weight 700, `-0.04em`.
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

Group labels are 12px sentence-case muted text. The rail has a 2px `--outline`
right edge. Active navigation: a square `--highlight-solid` block with a 2px
`--highlight-solid-ink` left rule, weight 600 versus inactive 400. No pill. Keep
`aria-current`. Navigation icon glyphs are visually hidden in the text-only rail.

At 900px the margin column and split layouts stack under their main column. At
700px the existing drawer replaces the rail; mobile insets are 16px. Buttons,
selects, text inputs and navigation retain 44px minimum targets. Keep
Escape/backdrop dismissal, focus return and all aria attributes. No horizontal
page overflow at 320px or wider.

## 4a. The mark

The Routeweft mark is a plain weave: three warp threads and three weft threads, each going over, under, over. It is the
only ornament in the chrome. `ui/public/mark.svg` is the single hand-written drawing (32 x 32, even-unit edges so it lands
on whole pixels at 16px, no letters). `ui/src/Mark.tsx` inlines it so it inherits colour: the threads use `currentColor`
and one thread uses `var(--mark-accent, currentColor)`; `.brand-mark` sets `color: var(--text-main)` and
`--mark-accent: var(--live)`, so light and dark need no second asset. It sits at 20px beside the wordmark in the sidebar
and in the phone header. `ui/public/favicon.svg` is the same drawing with literal colours (a favicon cannot read the
page's CSS variables) and a `prefers-color-scheme: dark` block; `favicon-32.png` and `apple-touch-icon.png` (180px) are
rendered from it on the light ground. `ui/index.html` carries two `theme-color` tags equal to `--paper`, light `#ecebe4` and dark `#111210` (BDR-015 values
`#f4f4f2` / `#0c0e11` until phase 4 of #263).

## 5. Layout components

Implemented in `ui/src/editorial.tsx` and `ui/src/style.css`.

- **Row** (`.ed-row`): main column and margin column; `gap` prop picks 40/72/120px.
- **Main** with an `h2` section heading (16px, 500); **Margin** (`aside`, 300px);
  **Fact** (label 12px muted, value 16px 500, mono note); **Caption** (`Fig. 1`
  kicker in 600, then text); **Statement** (28px, with the action it drives).
- **Two separator weights do different jobs:** a 3px ink rule opens a section,
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
  panel). Never an empty state. It is the one `--tint-live` box: 2px `--outline`
  and the `--hard` shadow.
- Boxes, panels, tables and inputs have zero radius; buttons and chips 2px.
- Rules that open a section, margin list or table head are 3px ink; share bars
  are 6px.
- Tables collapse borders, use hairline row separators, never zebra stripes, and
  right-align numeric columns.

## 6. Data presentation

- **Daily chart (Fig. 1):** SVG, no chart library. Axis, gridlines, peak label,
  hover and arrow-key readout, and a screen-reader table. The latest 7 days are
  `--live` bars; a 2px tick marks the same day a week earlier; a dashed line is the
  period average; weekends are shaded. Hover shows the change against the week
  before.
- **Comparisons see only observed days.** The API omits a day with no attributed 2xx traffic, so a missing day is
  "not recorded", not zero. Charts draw a zero-filled calendar window for a continuous axis, but every comparison (the
  period average, week-over-week, the same-day-last-week ticks, the weekend ratio, the busiest day against the average)
  goes through `observeSeries()` in `ui/src/format.ts`, which keeps only the days from the first recorded day on.
  Week-over-week and the ticks need 14 observed days; before that the page says "N successful of M requests" and draws
  no ticks. A window label names the window it sums: "the latest 7 days" appears only when the window has 7 days and is not
  the 24-hour period, otherwise the page shows a per-day average. The comparison helpers accept only that type, so passing them a raw series does not compile
  (`ui/src/format.typecheck.ts` fails `npm run typecheck` if that changes), and a unit test checks that no result depends
  on the length of the zero-filled window.
- **Share bars** all use one baseline, `largest()` of the values present, never the first row.
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
- Empty: an outlined box (`--outline`, `--hard-sm`) with the reason and the action
  ("Show all time").
- Error: an outlined `--tint-danger` box with a 6px `--danger` left rule, the
  message and a Retry control; stale
  data stays on screen with the error above it. Failure of optional data (usage
  history, request log, connections) never blocks the overview itself.
- Preserve explicit errors, secrets and destructive confirmations.

## 8. Controls and Accessibility

Primary actions: paper-coloured text on an ink fill with a 2px `--outline` and
`--hard-sm`, dark solid in light mode and light solid in dark mode. Never blue.
Secondary actions have a 2px `--outline` on `--surface`; danger uses its state
token. Disabled controls remain visibly disabled. Active press moves 2px down and
right into the shadow and drops it.

Inputs/selects have square corners, a 2px `--border` boundary and readable labels. Keep
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
2. `rg -n 'backdrop-filter|blur\(|gradient' ui/src`: zero matches. Every
   `box-shadow` in `ui/src` is `var(--hard)`, `var(--hard-sm)` or `none`, and the
   two tokens have a zero blur radius. Radius token values are only 0/2px.
3. `git diff origin/main -- ui/src/main.tsx` is empty, except for the brand mark (#105): the import of `Mark` and the two
   `<Mark />` slots in the sidebar and the phone header.
4. Smoke asserts, on every page: only the sizes 11/12/16/28px (24px for statements
   on a phone), exactly one accent surface, no horizontal overflow, text contrast
   4.5:1 and state colours 4.5:1, no computed shell radius above 2px, every
   computed `box-shadow` a zero-blur offset, the 2px focus ring with 2px offset, the
   skeleton, error and empty states, and first-link keyboard focus. Do not weaken
   existing workflow assertions.
5. Overview, Usage and Providers screenshots at 1440px and 390px, light and dark,
   in `docs/evidence/263/<phase>/` (the BDR-015 set stays in `docs/evidence/102/final/`). They show a locally seeded database, never
   production traffic.
6. Check contrast, overflow, browser errors, drawer dismissal/focus return,
   persistence, OS-theme changes and reduced motion. Record checks not run.

`ROUTEWEFT_SMOKE_ARTIFACTS` selects the screenshot/report directory (default
`/tmp/routeweft-ui-evidence`). Smoke reuses existing control-API fixtures;
Chromium and `playwright-core` are required, as documented in
`ui/scripts/smoke.sh`.

Acceptance sentence: the page is recognisably Routeweft from the weave mark, the
square geometry and the mono figures alone; every box with an ink outline and a
hard shadow means something; nothing is blurred, glowing or gradient; and every
text pair keeps 4.5:1 and every control boundary 3:1 in both themes.

## 11. What #263 changed from the BDR-015 text

Quoted from the previous version (git `f15b350`), each replaced as above:

- §1: "Owner decisions #98 (graphite, flat, no decoration) and #102 (direction B,
  "editorial") define the style." Now #102 for layout and #263 for the surface.
- §1: "**Colour carries state only.**" Now "colour carries meaning, never
  decoration", adding the highlighter and three tints.
- §1: "No gradient, blur, glass, shadow or glow anywhere. Flat fills only." The
  hard offset shadow is allowed as two tokens; everything else stays forbidden.
- §2: every colour value (graphite paper `#f4f4f2` / `#0c0e11`, ink `#14161a`),
  and "light uses an ink slab (`#14161a`, text `#f4f4f2`), dark uses a raised slab
  (`#151b22`)" for the accent surface, now a `--tint-live` box. New tokens:
  `--outline`, `--shadow-ink`, `--hard`, `--hard-sm`, `--highlight`,
  `--highlight-solid`, `--highlight-solid-ink`, `--tint-live`, `--tint-warn`,
  `--tint-danger`.
- §3: "the page title is weight 300, `-0.025em`." Now 700, `-0.04em`; the
  headline numbers carry the marker.
- §4: "Active navigation: 2px `--live` left rule ... No active background pill."
  Now a square highlighter block with an ink rule.
- §4a: "`theme-color` tags, light `#f4f4f2` and dark `#0c0e11`."
- §5: "a 2px ink rule opens a section" (now 3px) and "It has a 2px `--live` left
  rule." (now outline plus `--hard`).
- §7: "Empty: a ruled box" and "Error: left rule in the danger colour". Now
  outlined boxes with `--hard-sm`.
- §8: "Secondary actions use `--border` boundaries" and "Active press moves down
  at most 1px." Now a 2px outline, and press moves 2px into the shadow.
- §10: "`rg -n 'gradient|backdrop-filter|box-shadow' ui/src`: zero matches." Now
  zero blur, backdrop filter and gradient, and `box-shadow` only as the tokens;
  screenshots move to `docs/evidence/263/`.

Unchanged: the editorial layout (§1, §5), the faces and the 11/12/16/28px scale
(§3), the navigation table, drawer, 44px targets and 320px rule (§4), the mark
drawing (§4a), status never shown by colour alone and every data rule (§6),
loading (§7), focus and accessibility including 4.5:1 text and 3:1 boundaries
(§8), themes and motion (§9).

