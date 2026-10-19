# Routeweft UI Style Contract

Status: **Normative for frontend work**

## 1. Graphite Rule

Owner decision #98 (2026-10-19) supersedes the LiteRouter v1 visual reference
and similarity acceptance rule. Routeweft owns its visual language and static
React + TypeScript + Vite implementation (SPEC section 23, BDR-003/015).
PRD sections 14-15 still govern navigation and operator workflows.

An instrument for routing operators: cool graphite, ink-on-paper actions,
square edges, hairlines and typographic hierarchy. Color communicates danger,
success, warning or information only. Flat fills only. No gradients, blur,
glass, shadows, glow, decorative color or marketing-scale hero cards.

Design read: operator console, ENERGY 1 / RHYTHM 2 / MOTION 1.
Ink contrast is the deliberate accent; the rule is the repeated identity motif.
Summary registers, detail rows and operational lists vary composition according
to the task, not a template. Whitespace separates sections, not floating cards.

## 2. Tokens

`ui/src/style.css` is the executable token source.

| Token | Light | Dark |
| --- | --- | --- |
| `--ink` | `#20242b` | `#eef1f5` |
| `--ink-hover` | `#383e47` | `#d0d6df` |
| `--paper`, `--bg`, `--surface` | `#f7f8fa` | `#171a20` |
| `--sidebar` | `#f0f2f5` | `#1c2027` |
| `--surface-2` | `#e9ecf0` | `#252b34` |
| `--surface-3` | `#d7dce3` | `#39414d` |
| `--border` | `#7b838f` | `#778291` |
| `--border-subtle` | `#c6ccd4` | `#3d4653` |
| `--text-main` | `#20242b` | `#eef1f5` |
| `--text-muted`, `--text-subtle` | `#505965` | `#b1bbc8` |
| `--danger` | `#ae2337` | `#ff91a0` |
| `--success` | `#12633e` | `#7bd6a4` |
| `--warning` | `#795000` | `#efc36b` |
| `--info` | `#205d94` | `#8dbff2` |

Control boundaries use `--border` for non-text contrast. Structural separators
use `--border-subtle`; they are never the sole identifier of a control.
State chips use state-colored text and borders, with explicit labels.
Notices may mix a state tint into the surface; never rely on color alone.

| Geometry / spacing | Value |
| --- | --- |
| `--radius-flat` | `0` |
| `--radius-control` | `2px`, buttons and small chips only |
| `--rail-width` | `13rem` |
| `--control-height` | `28px` desktop |
| `--row-height` | `32px` minimum for single-line rows |
| `--space-1/2/3/4/6/8` | `4/8/12/16/24/32px` |
| `--motion` | `120ms` maximum |

Panels, inputs, tables, selected rows and navigation have zero radius.
Wrapped rows grow with their content; fixed heights must not clip labels.

## 3. Typography

- Prose/headings: **Outfit**, a geometric sans with compact forms for operator
  labels, not marketing-scale typography. Fallback: `ui-sans-serif, system-ui,
  sans-serif`.
- Numeric/technical content: **JetBrains Mono**, retained for unambiguous IDs
  and stable alignment. Fallback: `ui-monospace, monospace`.
- Numbers, IDs, keys, timestamps and status tokens use mono, including metric
  notes, detail values, counts, chart labels and pagination.
- Tabular numerals inherit throughout the application.
- Labels: 10px, uppercase, tracking approximately `0.12em`.
- Values: approximately 28px, tight tracking. Page titles: 28px on every
  viewport, weight 600, tracking `-0.04em`. Each page uses exactly three computed
  font sizes: 28px (titles, values), 12px (body), 10px (labels, notes).
- Descriptions remain readable sans prose, not terminal output.

Fonts load through the existing Google Fonts stylesheet in `ui/index.html`;
fallbacks must remain usable without that service. Material Symbols Outlined
remains the action vocabulary. Navigation icon definitions and accessible
semantics remain intact, but glyphs are visually hidden in the text-only rail.
The existing letter tile and unmeasured service status dot are hidden; the
product name serves as the brand mark.

## 4. Shell and Navigation

Desktop: fixed 13rem rail, 1px right rule, workspace offset by the rail.
Content maximum width 110rem; desktop insets 28px vertical / 32px horizontal.

Keep groups, order, labels, icons and generated hashes byte-identical:

| Group | Items, in order |
| --- | --- |
| Operate | Overview; Endpoint & Key; Providers |
| Route | Combo & Capability Adapter; System One |
| Observe | Usage; Quota Tracker; Token Saver |
| System | Console Log; Settings |

Active navigation: 1px ink left rule, weight 600 versus inactive 400.
No active background pill. Neutral hover fill is allowed. Keep `aria-current`.

At 900px, summary/detail grids reduce columns and provider/Combo layouts stack.
At 700px, the existing drawer replaces the fixed rail; content becomes one
column. Mobile insets are 16px. Buttons, selects, text inputs and navigation
retain 44px minimum targets; checkbox/radio labels provide their hit area.
Keep Escape/backdrop dismissal, focus return and all aria attributes.
Visibility changes immediately; only opacity and 1px translation animate.
No off-screen slide or horizontal page overflow at 320px or wider.

## 5. Ruled Sections, Not Cards

There is no card primitive. Retain semantic sections without enclosing rounded
borders or separate elevated surfaces.

- Section heading/label row, 1px bottom rule, then content.
- Content and tables are full-bleed to section edges.
- Metrics form one ruled register with internal dividers, not separate tiles.
- Provider selection is a continuous ruled list; selection uses an ink edge
  and neutral fill. Preserve underlying button behavior.
- Tables collapse borders, use hairline row separators, never zebra stripes.
- Preserve explicit errors, secrets and destructive confirmations.
- Preserve loading, empty, stale-data, error and retry states.

## 6. Controls and Accessibility

Primary actions: paper-colored text on an ink fill, dark solid in light mode
and light solid in dark mode. Never blue. Secondary actions use neutral
boundaries; danger uses its state token. Disabled controls remain visibly
disabled. Active press moves down at most 1px.

Inputs/selects have square corners, readable labels and explicit boundaries.
Keep secrets masked, inline validation and native control semantics.
Do not reorder focusable markup or alter event handlers during visual work.

Every control has a visible 2px focus outline in the focus accent (`--focus`), 2px offset, both
themes. Each page has one accent surface (`.accent-surface`) whose control
borders (`--accent-control`) keep 3:1 against its fill.
Keep skip-to-content, landmarks, labels, `aria-live`, focus order and reduced
motion. Focus must not be clipped by scroll regions.
Normal text and state colors require WCAG AA 4.5:1; control boundaries and
focus require 3:1 against adjacent surfaces. No color-only feedback.

## 7. Themes and Motion

Ship light, dark and system. Preserve the pre-paint theme resolver and persisted
preference. System follows OS changes; native controls respect `color-scheme`.
Dark action hover must not become dark-on-dark.

Transitions affect opacity and translation only, never scale or spring.
Maximum 120ms; no continuous decorative animation. Reduced motion uses
effectively immediate transitions and disables repeated animation.

## 8. Acceptance and Evidence

Routeweft must look distinct from LiteRouter v1. Similarity is not acceptance.
Ticket #98 governs visuals; parity gaps from #77 remain separate.
No API paths, fetching, state logic, workflows or nav definitions change.

1. `npm run typecheck`, `npm test`, `npm run build`, `npm run smoke` pass.
2. `rg -n 'gradient|backdrop-filter|box-shadow' ui/src/style.css`: zero matches.
   Radius token values are only 0/2px.
3. `git diff 10ce866 -- ui/src/main.tsx`: empty, proving the entire navigation
   definition, hash generation and shell behavior remain unchanged.
4. Smoke asserts first-link keyboard focus, active ink marker/weight, and no
   computed shell radius above 2px, including pseudo-elements. Do not weaken
   existing workflow assertions.
5. Overview, Usage and Providers screenshots: 1440px and 390px, light and dark,
   plus system evidence. Identify sanitized smoke fixtures, never claim
   production traffic.
6. Check contrast, overflow, browser errors, drawer dismissal/focus return,
   persistence, OS-theme changes and reduced motion. Record checks not run.

`ROUTEWEFT_SMOKE_ARTIFACTS` selects the screenshot/report directory (default
`/tmp/routeweft-98-evidence`). Smoke reuses existing control-API fixtures;
Chromium and `playwright-core` are required, as documented in
`ui/scripts/smoke.sh`.
