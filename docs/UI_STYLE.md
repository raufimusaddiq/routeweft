# Routeweft UI Style Contract

Status: **Normative for frontend work**

> **Transition (#263, BDR-027).** The visual language below replaces the BDR-015
> graphite rule. The owner chose direction D "Signal" on 2026-10-27 from the
> phase 1 previews (`ui/preview/263/`, screenshots in
> `docs/evidence/263/preview/`); A, B and C were considered and not chosen
> (BDR-027). The tokens and rules below are D's. Phase 2 implements §2's tokens
> and the shell (rail, phone header, page title, buttons, form controls, tags,
> focus, the accent slab, empty and error states, `theme-color`, favicon) and the
> §12 guards. Phase 3a (2026-11-01) gives the page bodies of Overview, Endpoint &
> Key, Combo & Capability Adapter and System One their D layout (§5, "Page
> bodies"); phase 3b (2026-11-01) does the same for Providers, Usage and Quota
> Tracker; phase 3c part A (2026-11-01) for Token Saver, Console Log and Settings;
> the other pages (3c part B: Sign in, onboarding) and the charts and the mark (phase 4) still use their BDR-015 layout with D's tokens. Code, guards and this
> file change together, phase by phase.

## 1. House style: editorial instrument, retro neo-brutalist

Owner decision #102 (direction B, "editorial") defines the layout; owner decision
#263 (BDR-027, superseding the visual part of #98, direction D "Signal") defines
the surface: a poster. A black rail, raw oversized titles and headline numbers,
pill geometry, one pink slab with an asymmetric hard shadow, and the
data left open on the page with wide negative space. Routeweft owns its visual language and its static
React + TypeScript + Vite implementation (SPEC section 23, BDR-003/015). PRD
sections 14-15 still govern navigation and operator workflows.

The page reads like an instrument report, not a settings panel:

- **One sentence carries the headline number** at 44px (30px on a phone), with its
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
- **Colour carries meaning, never decoration.** Three colours plus ink and paper:
  cyan (`--live`) is data that is current (this week's bars); pink (`--slab`) is
  the one accent surface; yellow (`--highlight`) marks the number a sentence is
  about and the active place in the rail. State colours (`--danger`, `--success`,
  `--warning`) stay functional, always beside a word.
- **Data stays open.** Figures, tables and margin lists sit on the paper with
  heavy rules, no box. The outlined, shadowed objects are the slab, controls,
  tags, and empty and error states.
- **Depth is a hard offset shadow, never a blur.** `--hard` (10px right, 6px down,
  0 blur) on the slab, `--hard-sm` (5px, 3px, 0) on controls and states. No
  gradient, blur, glass, glow or backdrop filter anywhere. Flat fills only.

## 2. Tokens

`ui/src/style.css` is the executable token source. Light and dark set the same
names. Ratios are WCAG 2, measured against `--paper` unless noted; text pairs
need 4.5:1, control and shell boundaries 3:1.

| Token | Light | Dark |
| --- | --- | --- |
| `--paper`, `--bg` | `#f7f6f0` | `#111111` |
| `--surface` (inside a control or state box) | `#ffffff` | `#1b1b1b` |
| `--surface-2` / `--surface-3` | `#ecebe3` / `#d4d2c8` | `#262626` / `#3a3a3a` |
| `--ink`, `--text-main` | `#111111` (17.4:1; 18.9:1 on surface) | `#f7f6f0` (17.4:1; 15.9:1 on surface) |
| `--ink-hover` | `#333333` | `#d6d4cc` |
| `--text-muted`, `--text-subtle` | `#45443f` (9.0:1) | `#b5b3aa` (9.0:1; 8.2:1 on surface) |
| `--border`, `--outline` (control and box boundaries) | `#111111` (17.4:1) | `#f7f6f0` (17.4:1) |
| `--border-subtle` (row hairlines, not a boundary) | `#d4d2c8` | `#333333` |
| `--shadow-ink` | `#111111` | `#f7f6f0` |
| `--danger` | `#b0001a` (6.8:1) | `#ff6b6b` (6.8:1) |
| `--success` | `#0b6b2e` (6.1:1) | `#5fd38a` (10.0:1) |
| `--warning` | `#7a5200` (6.4:1) | `#ffc14d` (11.7:1) |
| `--live`, `--focus` | `#00727f` (5.2:1) | `#4fd1e0` (10.4:1) |
| `--slab` (the accent surface) | `#ff5fa2`; ink on it 6.7:1; its edge is the 3px ink outline (17.4:1) | `#ff6fae`; ink `#111111` on it 7.3:1; 7.3:1 against the page |
| `--highlight` / `--highlight-ink` (marker block, active nav pill) | `#ffd60a` / `#111111` (13.4:1) | same |
| `--rail` (sidebar and phone header) | `#111111` (17.4:1 against the page) | `#000000` (1.1:1, so `--rail-rule` draws the edge) |
| `--rail-text` / `--rail-muted` | `#f7f6f0` (17.4:1) / `#c9c7bd` (11.1:1) | `#f7f6f0` (19.4:1) / `#c9c7bd` (12.4:1) |
| `--rail-rule` | `#111111`, the rail's own colour, so no visible rule | 3px `#f7f6f0` (17.4:1 against the page) |
| `--hard` / `--hard-sm` | `10px 6px 0 var(--shadow-ink)` / `5px 3px 0 var(--shadow-ink)` | same |

The accent surface re-points the tokens onto the slab: text `#111111` (6.7:1
light, 7.3:1 dark), muted `#3a0a20` (6.0:1 / 6.5:1), and every control, rule,
focus, live and state colour inside it becomes `#111111`. A control inside the
slab therefore draws its 3px boundary and shadow in `#111111`, not the page's
`--outline`. Status inside the slab is a mark plus a word in ink.

| Geometry / spacing | Value |
| --- | --- |
| Radius | `999px` pills: nav items, buttons, inputs, selects, state tags, share bars, highlight blocks. `50%` on status dots. `32px` the slab (`24px` on a phone), `24px` empty and error states, `22px` provider rows, `20px` textareas. Nothing else is rounded. |
| Outline | `3px solid var(--outline)` on the slab, buttons, inputs and states; 2px `currentColor` on state tags; 1px `--border-subtle` hairlines between rows |
| Rules | 6px ink rule opens a section, statement and margin list; 4px under a table head |
| Shadow | `--hard` on the slab; `--hard-sm` on primary and secondary buttons, empty and error states; nothing else |
| `--rail-width` | `12.5rem` |
| `--control-height` | `28px` desktop, 44px on a phone |
| `--row-height` | `32px` minimum for single-line rows |
| Section gap above a row | `.gap-s` 40px, `.gap-m` 72px, `.gap-l` 120px; 56 / 48 and 80 / 64px below 900 / 700px |
| `--motion` / `--hover` | `120ms` entry, `80ms` hover; `cubic-bezier(0.2, 0, 0, 1)` |

Spacing is varied on purpose so sections group. Wrapped rows grow with their
content; fixed heights must not clip labels.

## 3. Typography

- **Space Grotesk** for headings, body and large figures; **JetBrains Mono** for
  tables, technical values, IDs, timestamps, chart text and labels. Both are
  self-hosted from `ui/public/fonts/` as latin variable woff2 with
  `font-display: swap` (licence note beside them). Fallbacks:
  `ui-sans-serif, system-ui, sans-serif` and `ui-monospace, monospace`. Only the
  Material Symbols stylesheet still loads from Google Fonts.
- Scale, desktop: **64px** page title (`--t-poster`), **56px** the live figure,
  **44px** headline (`--t-poster-lead`), **36px** statement
  (`--t-poster-statement`), **28px** secondary display (provider name, panel
  figures), **16px** section headings and row names, **12px** body, labels,
  captions and table text, **11px** chart axis ticks only. Phone (below 700px):
  title 40px, live figure 40px, headline 30px, statement 26px; 24px and 28px stay
  available for secondary display. Nothing is below 11px.
- Titles are weight 700, line height 0.95, tracking `-0.055em`, and break only
  between words. Every page name fits at 320px: the longest, Combo & Capability
  Adapter, takes three lines at 320px, two at 390px and one at 1440px.
- Labels (nav groups, table heads, fact labels, kickers) are 12px mono capitals,
  tracking `0.08em`, muted.
- Headline and statement: weight 400, the key numbers weight 700 on a
  `--highlight` pill block (`--highlight-ink` text), not a gradient.
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

The rail is a black slab (`--rail`) in both themes; in dark it has a 3px
`--rail-rule` right edge so it does not melt into the near-black page, and the
phone header has the same rule on its bottom edge. Group labels are 12px mono
capitals, muted. Active navigation: a `--highlight` pill (999px) with
`--highlight-ink` text and a 2px `--highlight-ink` marker, weight 700 versus
inactive 400. Keep `aria-current`. Navigation icon glyphs are visually hidden in the text-only rail.

At 900px the margin column and split layouts stack under their main column. At
700px the existing drawer replaces the rail; mobile insets are 16px. On a phone
the page heading row wraps: a long title (Combo & Capability Adapter takes three
lines at 320px) pushes Sign out onto its own line instead of squeezing it. The
frameless menu button's bars are paper on the black header (3:1 or better). Buttons,
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
rendered from it on the light ground. In the rail the mark is paper-coloured with a `--highlight` thread. The favicon
keeps the drawing with D's literal colours: light warp `#111111`, weft `#00727f`; dark warp `#f7f6f0`, weft `#4fd1e0`.
`ui/index.html` carries two `theme-color` tags equal to `--paper`: light `#f7f6f0` and dark `#111111` (BDR-015 values
`#f4f4f2` / `#0c0e11` until phase 4 of #263).

## 5. Layout components

Implemented in `ui/src/editorial.tsx` and `ui/src/style.css`.

- **Row** (`.ed-row`): main column and margin column; `gap` prop picks 40/72/120px.
- **Main** with an `h2` section heading (16px, 500); **Margin** (`aside`, 300px);
  **Fact** (mono capital label, value 16px 500, mono note); **Caption** (`FIG. 1`
  kicker, then text); **Statement** (36px, with the action it drives).
- **Two separator weights do different jobs:** a 6px ink rule opens a section,
  statement or margin list (4px under a table head); 1px `--border-subtle` rules
  separate rows.
- **Status is never colour alone:** a 10px round dot (`.dot`, 50% radius) always
  beside its word. #263 first froze "square dot plus word"; the owner lifted the
  shape part on 2026-10-28, so the mark follows D. The word beside it stays
  frozen. One
  classification serves every mark: HTTP 2xx good, 3xx idle (a hollow mark), 4xx warn, 5xx bad.
  A provider's mark is red under the same rule as its error figure (below).
  A *failure* is any response that is not a 2xx, the API's own definition of
  `errors` (`usage_summary.go`), so "failed", "Succeeded" and "Largest failure"
  always agree; redirects count in those totals though their mark is neutral.
  The rule lives in `isFailure()` in `ui/src/format.ts`.
- **Page bodies (`.page-d`, phase 3a).** A page opts in by adding `page-d` to its
  wrapper; 3b did Providers, Usage and Quota, 3c part A Token Saver, Console Log and Settings, part B adds the rest. It sets: sections of a page 72px apart
  (56px below 900px, 48px below 700px; a toolbar and what it controls 32px); a 6px
  ink rule opens a section heading, statement, margin list and group, a 4px rule
  sits under a table head, and a heading inside the accent surface keeps a hairline
  instead; the headline and statement are weight 400 with the number they are about
  weight 700 on a `--highlight` block (a pill, text `--highlight-ink`); copyable
  values and fields are 3px-outlined pills; example lists and access lists are open
  rows between hairlines, not boxed grids; a choice (model access) is a 24px
  outlined box and the chosen one a `--highlight` block with the radio still
  checked and its words unchanged; the selected Combo row is a 22px `--highlight`
  block that re-points the tokens like the accent surface (state stays a word);
  a notice, like an empty or error state, is a 3px outlined 24px box with
  `--hard-sm` in its own colour; the request-log route is one line ending in an
  ellipsis with the full route as its `title` (on a phone it takes the whole first
  line of the stacked row).
- **Accent surface** (`.accent-surface`): exactly one per page, marking the most
  important live thing (Overview: the live card; Usage: the daily chart plate;
  Providers: the provider summary banner; other pages: their toolbar or primary
  panel). Never an empty state. It is the pink slab: `--slab` fill, 3px
  `--outline`, 32px radius (24px on a phone), `--hard`.
- Tables collapse borders, use hairline row separators, never zebra stripes, and
  right-align numeric columns. Provider rows are open; the selected row is a 22px
  `--highlight` block.
- **Stamped tags** (`.state-tag`): 2px `currentColor` pill, bold mono, tilted
  `-3deg`, with the word in its own case. Not CSS capitals: `text-transform`
  changes `innerText`, which copy and the smoke tests read.

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
- **Provider table (Table 1):** provider with status dot, requests, a thin
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
- Empty: a 3px `--outline` box, 24px radius, `--hard-sm`, with the reason and the
  action ("Show all time").
- Error: a 3px `--danger` box, 24px radius, `--hard-sm`, the message and a Retry
  control; stale data stays on screen with the error above it. Failure of
  optional data (usage history, request log, connections) never blocks the
  overview itself.
- Preserve explicit errors, secrets and destructive confirmations.

## 8. Controls and Accessibility

Buttons are 3px-outlined pills with `--hard-sm`. Primary: paper text on an ink
fill. Secondary: ink text on `--surface`. Danger: its state token as the outline.
Never blue. Disabled controls remain visibly disabled. Active press moves 5px
right and 3px down into the shadow and drops it.

Inputs and selects are 3px `--border` pills; textareas have a 20px radius.
Readable labels, masked secrets, inline validation and native control semantics
stay. Do not reorder focusable markup or alter event handlers during visual work.

Every control has a visible 2px focus outline in `--focus`, 2px offset, both
themes (inside the slab it is `#111111`; in the rail it is `--highlight`, 13.4:1
on black). On a chosen access-mode label, which is a highlight block, the radio's
ring is `--highlight-ink` instead, because `--focus` measures 1.29:1 on that
block in dark. A selected Provider or Combo row re-points `--focus` for what sits
inside it, so its own ring uses `--focus-page` (the page focus colour) and stays
visible on the page behind the block instead of 1:1 in dark. Keep skip-to-content,
landmarks, labels, `aria-live`, focus order and reduced motion. Focus must not be clipped by scroll regions. Normal text and
state colours require WCAG AA 4.5:1; control boundaries, the frameless menu
button's bars, the rail and header edges and focus require 3:1 against what is
behind them.

### Display language control (#279)

The language button (sidebar footer and the Settings page) is a 44px control with the language's
flag and its own name, outlined like a select; its dialog is a 3px-outlined slab with `--hard`, a
plain `rgba` scrim (no blur) and a grid of 44px options. The current language is stated in text
("(current language)") and by a heavier outline, not by colour alone. The names are not translated
(`data-i18n-skip`). Focus is trapped in the dialog, Escape and the scrim close it, and focus returns
to the button. See SPEC 23.1.

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
   two tokens have a zero blur radius. Radius values are only those in §2.
3. `git diff origin/main -- ui/src/main.tsx` is empty, except for the brand mark (#105): the import of `Mark` and the two
   `<Mark />` slots in the sidebar and the phone header.
4. Smoke asserts what §12 specifies. Do not weaken existing workflow or a11y
   assertions.
5. Screenshots of every changed page at 1440px and 390px, light and dark, with
   the drawer and selections clicked, in `docs/evidence/263/<phase>/` (the
   BDR-015 set stays in `docs/evidence/102/final/`). They show a locally seeded
   database, never production traffic.
6. Check contrast, overflow, browser errors, drawer dismissal/focus return,
   persistence, OS-theme changes and reduced motion. Record checks not run.

`ROUTEWEFT_SMOKE_ARTIFACTS` selects the screenshot/report directory (default
`/tmp/routeweft-ui-evidence`). Smoke reuses existing control-API fixtures;
Chromium and `playwright-core` are required, as documented in
`ui/scripts/smoke.sh`. CI runs the whole of it as the `ui-smoke` job on every push
and pull request (Playwright pinned to 1.49.1, installed outside the manifest, its
browsers cached by version; the evidence directory is uploaded as an artifact), so
the contrast, focus, overflow, drawer and workflow assertions above are checked
before merge; `make ui-smoke` runs the same locally.

Acceptance sentence: the page is recognisably Routeweft from the weave mark, the
black rail and the mono figures; one pink slab per page carries the live thing;
titles and key numbers are poster-sized and never overflow or split a word at
320px; nothing is blurred, glowing or gradient; and every text pair keeps 4.5:1
and every control, shell edge and focus ring 3:1 in both themes.

### Phase 3a guard (`ui/scripts/phase3a-pages-smoke.cjs`)

Run by `npm run smoke`. It loads Overview, Endpoint & Key, Combo & Capability
Adapter and System One with seeded data (the long Combo title, a long route) at
1440, 390 and 320 in light and dark, plus the drawer, the key access editor with a
choice made, the Combo edit form and the language dialog, and measures each view:
no horizontal overflow, no text under 4.5:1, no control boundary under 3:1 (the
frameless menu button is judged by its bars against the header, §12.10), no phone
control under 44px, no blur, and every `box-shadow` a `--hard`/`--hard-sm` offset
with zero blur and spread. With `PHASE3A_CAPTURE=1` and
`ROUTEWEFT_SMOKE_ARTIFACTS` it writes the screenshots kept in
`docs/evidence/263/phase3a/` (`before-` and `after-`, and `phase3a-*.json`).
Phases 3b and 3c add their pages to their own scripts.

### Phase 3b guard (`ui/scripts/phase3b-pages-smoke.cjs`)

Run by `npm run smoke`, same measurements as the 3a guard, for Providers, Usage and
Quota Tracker at 1440, 390 and 320 in light and dark, plus the states: provider details,
the Add connection form, the Generic Provider form, the Usage Requests and Request
details tabs, Codex credit expiry and the drawer. Page rules added in 3b (2026-11-01):
the selected provider row is the 22px `--highlight` block (the `.provider-option`
radius owner); the Usage tabs are `button-ghost` pills (the existing pill owner) with a 3px edge, the chosen one a `--highlight` block at
weight 700 with `aria-selected`; provider detail panels open on a 6px rule, except the empty "Select a provider" one, which is the 3px outlined 24px state box (`.provider-empty`, a `--radius-state` owner in the shell guard and measured by the 3b guard); the
topology hub is an outlined pill and its nodes open rows (an active node a 3px ink
rule, still saying "in flight"); a quota account is an open row under a 4px rule with
its windows behind a 3px ink rule.

### Keyboard, label and phone guard (`ui/scripts/a11y-keyboard-labels-smoke.cjs`)

Run by `npm run smoke`. It loads Overview, Endpoint & Key, Providers, Combo &
Capability Adapter, System One, Settings and Usage, plus the provider connection
form, at 1440 and 390 in light and dark (32 views, 664 controls), and asserts
from the rendered DOM: Tab reaches every control once in DOM order (a radio group
is one stop; roving `tabindex` respected; no positive `tabindex`), every control
shows a solid outline of at least 2px at 3:1 or more against the backdrop, Enter
activates buttons and links, Space activates buttons, checkboxes and unchecked
radios, every input/select/textarea/button/link has an accessible name, the
language dialog is the only focus trap and returns focus on Escape, nothing
overflows at 390 or 320 (16 views), and at 390 the focused field stays inside a
420px visual viewport with no fixed bar over it. It click-counts activation with
the default action prevented, so nothing navigates, submits or saves. The control
API is stubbed; no credential or provider is contacted.

## 11. What #263 changed from the BDR-015 text

Quoted from the previous version (git `f15b350`), each replaced as above:

- §1: "Owner decisions #98 (graphite, flat, no decoration) and #102 (direction B,
  "editorial") define the style." Now #102 for layout and #263 (D) for the surface.
- §1: "**Colour carries state only.**" Now three meaningful colours (live,
  slab, highlight) plus functional state colours.
- §1: "No gradient, blur, glass, shadow or glow anywhere. Flat fills only." The
  hard offset shadow is allowed as two tokens; everything else stays forbidden.
- §2: every colour value (graphite paper `#f4f4f2` / `#0c0e11`, ink `#14161a`),
  and the accent "ink slab" / "raised slab", now the pink slab. New tokens:
  `--outline`, `--shadow-ink`, `--hard`, `--hard-sm`, `--slab`, `--highlight`,
  `--highlight-ink`, `--rail`, `--rail-text`, `--rail-muted`, `--rail-rule`,
  `--t-poster`, `--t-poster-lead`, `--t-poster-statement`.
- §2: "`--radius-flat` `0`" and "`--radius-control` `2px`, buttons and small chips
  only". Now pills, the slab radius and the state radii listed in §2.
- §3: "Scale: **28px** ... **16px** ... **12px** ... **11px**", "Labels are
  sentence case, muted, not uppercase and not letter-spaced" and "the page title is
  weight 300". Now the poster scale, mono capital labels and 700 titles.
- §4: "Active navigation: 2px `--live` left rule ... No active background pill."
  Now a yellow pill on a black rail.
- §4a: "`theme-color` tags, light `#f4f4f2` and dark `#0c0e11`."
- §5: "Status is never colour alone: an 8px square (`.dot`) beside the word." Now
  a 10px round dot (owner, 2026-10-28); the word beside it is unchanged.
- §5: "a 2px ink rule opens a section" (now 6px) and "It has a 2px `--live` left
  rule." (now the pink slab).
- §7: "Empty: a ruled box" and "Error: left rule in the danger colour". Now 24px
  outlined boxes with `--hard-sm`.
- §8: "Primary actions: paper-coloured text on an ink fill", "Secondary actions use
  `--border` boundaries", "Active press moves down at most 1px" and "Inputs/selects
  have square corners". Now 3px pills, press 5x3px into the shadow.
- §10: "`rg -n 'gradient|backdrop-filter|box-shadow' ui/src`: zero matches" and the
  smoke list. Now §10.2 and §12.

Unchanged: the editorial layout (§1, §5), the navigation table, drawer, 44px
targets and 320px rule (§4), the mark drawing (§4a), status never shown by
colour alone (the mark beside its word; the mark is now round) and every data
rule (§5, §6), loading (§7), 4.5:1
text and 3:1 boundaries (§8), themes and motion (§9).

## 12. Phase 2 guard specification (D)

A specification, not code: phase 2 changes `ui/scripts/shell-smoke.cjs` in the
same PR as `ui/src/style.css`. Line numbers are at `b3ce095`; PR #267 also edits
this file, so re-locate them before editing. No assertion is loosened: each one
below is replaced by an equally strict check of the new value, or kept.

Rewritten:

1. **Corner radius** (`shell-smoke.cjs:55-58`, assert `:74`). Today: no shell
   element or pseudo-element has a radius above 2px. After: every computed corner
   radius of every shell element and its `::before`/`::after` is in the allow-list
   `0px`, `2px`, `20px`, `22px`, `24px`, `32px`, `999px`, `50%`, and each value is
   only on its owner from §2 (pills on `.nav-link`, buttons, inputs, selects,
   `.state-tag`, `.ed-bar`, highlight blocks; `32px`/`24px` on `.accent-surface`;
   `24px` on empty and error states; `22px` on `.provider-option`; `20px` on
   `textarea`; `50%` only on `.dot`, the round status mark the owner chose on
   2026-10-28). Every visible `.dot` is round (`50%`), and its parent carries a
   word beside it (status is never colour alone).
   Any other value fails with the element and corner named.
2. **Type sizes** (`:60-61`, asserts `:77-80`; per page `:1438-1441`, assert
   `:1558`). Today: 11/12/16/28px (24px on a phone), 12 and 28 required, 11 only in
   SVG. After: desktop allows 11/12/16/28/36/44/56/64px; phone allows
   11/12/16/24/26/28/30/40px. Required on every page: 12px and the title size (64px
   desktop, 40px phone). 11px stays reserved for SVG axis text (`:80` unchanged).
   The per-page loop `:1558` uses the same desktop list.
3. **Error state** (`:1087`, assert `:1553`). Today: left border `3px`. After:
   all four border widths `3px`, border colour equal to the computed `--danger`,
   radius `24px`, `box-shadow` equal to `--hard-sm`, and the Retry button present
   (`:1554` unchanged).
4. **One accent surface** (`:62`, asserts `:81`, `:1559`). Kept: exactly one
   visible `.accent-surface` per view and per page. Added: its background is the
   computed `--slab`, border `3px`, radius `32px` (`24px` below 700px),
   `box-shadow` equal to `--hard`, and its text 4.5:1 on the slab.
5. **Active nav** (`:63-69`, asserts `:82-84`). Kept: the `::before` marker is
   `2px` wide and not transparent, active weight above inactive. Added: the active
   link's background is the computed `--highlight`, radius `999px`, and its text
   4.5:1 on it.
6. **Focus ring** (`:88-95`, `:1082`, asserts `:1548-1551`). Kept: `2px solid`,
   `2px` offset, focused and `:focus-visible`. Fix: `:90` compares against the
   literal `rgb(32, 36, 43)`, which is neither the BDR-015 ink nor D's; it must
   compare against the computed `--ink`. Added: the ring colour is 3:1 against
   the background behind the focused control (`--highlight` on the black rail).
7. **theme-color** (`:493`, assert `:1449`). Kept: the two media queries. Added:
   `content` is `#f7f6f0` for light and `#111111` for dark, equal to `--paper`.
8. **Favicon** (`ui/public/favicon.svg:6-10`; hrefs asserted at `:1450`). Added:
   read `/favicon.svg` and assert the literal colours from §4a (light warp
   `#111111`, weft `#00727f`; dark warp `#f7f6f0`, weft `#4fd1e0`).

New assertions:

9. **Control boundary 3:1** in `captureVisuals` (both themes, 1440 and 390):
   every visible button, input, select and textarea with a non-transparent border
   has its border, or its own fill, at 3:1 or better against the background
   behind it. A transparent border is not a boundary and is skipped; a text-only
   control is judged by its text at 4.5:1.
10. **Menu button against the header**: below 700px, the `.mobile-menu` bars
    (`color`) are 3:1 against `.mobile-header`'s background.
11. **Shell edges**: the desktop `.sidebar` and the phone `.mobile-header` are
    3:1 against the page, from their fill or their edge rule.
12. **Shadows**: every computed `box-shadow` on a shell element is `none` or one
    of the two token offsets (10px 6px or 5px 3px) with zero blur and zero
    spread. The colour is not compared: inside the slab a control's shadow is the
    slab's ink, not `--shadow-ink`.
13. **Titles**: at 320px on every nav page, the `h1` neither overflows its box
    nor the page, and no word in it is split across lines.

Kept unchanged and never loosened: text contrast 4.5:1 (`:51-54`, `:75`), state
colours 4.5:1 (`:59`, `:76`), the a11y audit (`:1328-1425`, `:1657-1665`), 320px
overflow (`:1656`), reduced motion (`:1666-1669`), the empty-state border
(`:1556`), 44px targets and every workflow assertion.

### Phase 3c part A guard (`ui/scripts/phase3ca-pages-smoke.cjs`)

Run by `npm run smoke`, same measurements as the 3a and 3b guards, for Token Saver,
Console Log and Settings at 1440, 390 and 320 in light and dark, plus an edited
Headroom field, an expanded console line (1440 and 390), the level filter, the
language dialog opened from Settings (1440 and 390) and the drawer. Page rules added
in 3c part A (2026-11-01): feature, toggle and helper rows are open rows under a
hairline with 16px/700 names and 64px minimum height; the PXPIPE counters are 28px/700
panel figures; a console line's detail is an open block with a 3px ink edge; the
Settings JSON editors are 20px 3px-outlined textareas; a warning keeps its edge and
its words. Two 44px phone targets the baseline missed are fixed here: the language
dialog's close control and the Settings backup download link.
