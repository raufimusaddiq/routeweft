# #263 phase 1 preview: retro neo-brutalist directions

**Preview only.** Nothing here ships: no file in `ui/src` imports this directory,
`tsconfig.json` includes only `src`, and `vite build` starts from `index.html`.
Each direction is an overlay stylesheet that `capture.cjs` injects over the
built `style.css` in a browser, so the screenshots show the real Overview and
Providers markup, the real nav and the real drawer, only restyled. The
screenshots carry a magenta "PREVIEW #263" tag.

| Direction | File | In one line | Trade-off |
| --- | --- | --- | --- |
| A "Loom" | `a-loom.css` | Square 0/2px kept. Linen paper, an ink outline and a hard 4px shadow on the boxes that mean something, a lime highlighter, a cyan-tinted live card. | Fewest guard changes, but the owner judged it not brutal enough (2026-10-27). |
| B "Patchwork" | `b-patchwork.css` | Rounded 12px tiles around every figure, pill buttons, an ink-filled rail, sage paper, an orange highlighter, pastel patches. | The loudest of round 1, still soft; rewrites the radius guard. |
| C "Ledger" (recommended) | `c-ledger.css` | Square everything. A heavy ruled grid: every table cell, list row and control boxed in 2-4px black, inverted black table heads, an inverted black nav block, a vermilion slab with an 8px hard shadow, acid-yellow highlighter blocks, mono capital labels, stamped tags, buttons that press 4px into their shadow. | Hard and honest, and it stays inside the type-scale, radius and nav guards. It is dense: every datum sits in a box. |
| D "Signal" | `d-signal.css` | A poster. A black rail with a yellow pill for the active page, oversized raw titles (64px, 40px on a phone) and headline numbers (44px), pills and circles pushed to the extreme (status circles, pill controls, 32px slab), one pink slab with an asymmetric 10x6px shadow, tilted stamped tags, open data with wide negative space. | The most anti-mainstream. Rewrites the radius and type-scale guards, and the 64px title is unproven on the long page names that were not previewed. |

C and D differ in structure, not only colour: C boxes the data in a black grid
and keeps the type small and square; D removes the boxes from the data, makes
the type and the one slab huge, and goes round. Each uses three colours plus ink
and paper (C: vermilion, acid yellow, cyan live; D: pink, yellow, cyan live).
The state colours (danger, success, warning) stay functional in both, and status
is still a mark plus a word.

**Recommendation: C "Ledger".** It answers "not brutal enough" with the hardest
surface of the four while keeping what is Routeweft's own (square instrument
geometry, the weave mark, mono figures, the editorial layout). It reads as the
square sibling of the rounded Richmod and Antares. It changes one guard, the same
one A changes (the error-state border). D is the bolder poster, but it rewrites
two guard families, and oversized type is the riskiest choice for pages nobody
has seen yet (Combo & Capability Adapter, Settings).

## Run

```bash
cd ui
npm ci && npm install --no-save --no-package-lock playwright-core@1.49.1
npx playwright-core install chromium
npm run build
setsid npx vite preview --host 127.0.0.1 --port 4173 & echo $!   # stop it by PID afterwards
ROUTEWEFT_SMOKE_CHROME=<chromium> node preview/263/capture.cjs /tmp/routeweft-263-preview [current|a|b|c|d]
```

The API is stubbed with sanitized fixtures (no server, no credentials, no
production data). The page clock is frozen at 2026-09-25T12:00Z.

## Screenshots

`docs/evidence/263/preview/`, per direction (`a-`, `b-`, and from round 2 `c-`, `d-`):

- `<d>-overview-{light,dark}-{1440,390}.png`, `<d>-providers-{light,dark}-{1440,390}.png`: full page.
- `<d>-drawer-{light,dark}-390.png`: the phone drawer opened from the menu button.
- `<d>-providers-selected-{light,dark}-390.png`: openrouter selected in the catalogue, with its detail panels.
- `<d>-providers-focus-{light,dark}-1440.png`: keyboard focus on the first nav link (Tab from the brand).

The rail ends partway down the full-page desktop shots because the sidebar is
`position: fixed` and is painted once at the top. The current UI does the same.

## Measured (capture.cjs, same algorithms as shell-smoke.cjs)

Overflow is `scrollWidth - innerWidth`. Low text is the count of text below
4.5:1. Weak controls are control boundaries below 3:1. Small targets are phone
controls under 44px. Rounded is the count of elements with a radius above 2px.
For every row in all four directions: no browser errors, Escape closes the drawer
and returns focus to `mobile-menu`, and the provider selection works. On a phone,
the bars of the frameless menu button count as its boundary (added in round 2).
The A and B rows are from round 1; A and B were not changed.

| Dir | View | Theme | Width | Overflow | Low text | Weak controls | Small targets | Rounded | box-shadow values | Blur | Accent surfaces |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A | overview | light | 1440 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px | 0 | 1 |
| A | overview | light | 390 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px | 0 | 1 |
| A | overview | light | 320 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px | 0 | 1 |
| A | overview | dark | 1440 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px | 0 | 1 |
| A | overview | dark | 390 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px | 0 | 1 |
| A | overview | dark | 320 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px | 0 | 1 |
| A | providers | light | 1440 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px; rgb(23, 24, 26) 2px 2px 0px 0px | 0 | 1 |
| A | providers | light | 390 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px; rgb(23, 24, 26) 2px 2px 0px 0px | 0 | 1 |
| A | providers | light | 320 | 0 | 0 | 0 | 0 | 0 | rgb(23, 24, 26) 4px 4px 0px 0px; rgb(23, 24, 26) 2px 2px 0px 0px | 0 | 1 |
| A | providers | dark | 1440 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px; rgb(93, 96, 85) 2px 2px 0px 0px | 0 | 1 |
| A | providers | dark | 390 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px; rgb(93, 96, 85) 2px 2px 0px 0px | 0 | 1 |
| A | providers | dark | 320 | 0 | 0 | button-ghost sign-out 1.12 | 0 | 0 | rgb(93, 96, 85) 4px 4px 0px 0px; rgb(93, 96, 85) 2px 2px 0px 0px | 0 | 1 |
| B | overview | light | 1440 | 0 | 0 | 0 | 0 | 30 | rgb(15, 36, 33) 3px 3px 0px 0px | 0 | 1 |
| B | overview | light | 390 | 0 | 0 | 0 | 0 | 20 | rgb(15, 36, 33) 3px 3px 0px 0px | 0 | 1 |
| B | overview | light | 320 | 0 | 0 | 0 | 0 | 20 | rgb(15, 36, 33) 3px 3px 0px 0px | 0 | 1 |
| B | overview | dark | 1440 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 30 | rgb(0, 0, 0) 3px 3px 0px 0px | 0 | 1 |
| B | overview | dark | 390 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 20 | rgb(0, 0, 0) 3px 3px 0px 0px | 0 | 1 |
| B | overview | dark | 320 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 20 | rgb(0, 0, 0) 3px 3px 0px 0px | 0 | 1 |
| B | providers | light | 1440 | 0 | 0 | 0 | 0 | 29 | rgb(15, 36, 33) 3px 3px 0px 0px; rgb(15, 36, 33) 2px 2px 0px 0px | 0 | 1 |
| B | providers | light | 390 | 0 | 0 | 0 | 0 | 19 | rgb(15, 36, 33) 3px 3px 0px 0px; rgb(15, 36, 33) 2px 2px 0px 0px | 0 | 1 |
| B | providers | light | 320 | 0 | 0 | 0 | 0 | 19 | rgb(15, 36, 33) 3px 3px 0px 0px; rgb(15, 36, 33) 2px 2px 0px 0px | 0 | 1 |
| B | providers | dark | 1440 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 29 | rgb(0, 0, 0) 3px 3px 0px 0px; rgb(0, 0, 0) 2px 2px 0px 0px | 0 | 1 |
| B | providers | dark | 390 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 19 | rgb(0, 0, 0) 3px 3px 0px 0px; rgb(0, 0, 0) 2px 2px 0px 0px | 0 | 1 |
| B | providers | dark | 320 | 0 | 0 | button-ghost sign-out 1.15 | 0 | 19 | rgb(0, 0, 0) 3px 3px 0px 0px; rgb(0, 0, 0) 2px 2px 0px 0px | 0 | 1 |

Round 2, with the computed type sizes (the guard allows 11/12/16/28, plus 24 on a phone):

| Dir | View | Theme | Width | Overflow | Low text | Weak controls | Small targets | Rounded | box-shadow values | Blur | Accent surfaces | Type sizes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| C | overview | light | 1440 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 28px |
| C | overview | light | 390 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 24px 28px |
| C | overview | light | 320 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 24px 28px |
| C | overview | dark | 1440 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 28px |
| C | overview | dark | 390 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 24px 28px |
| C | overview | dark | 320 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 11px 12px 16px 24px 28px |
| C | providers | light | 1440 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 12px 16px 28px |
| C | providers | light | 390 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 12px 16px 24px 28px |
| C | providers | light | 320 | 0 | 0 | 0 | 0 | 0 | rgb(0, 0, 0) 8px 8px 0px 0px; rgb(0, 0, 0) 4px 4px 0px 0px | 0 | 1 | 12px 16px 24px 28px |
| C | providers | dark | 1440 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(242, 240, 232) 4px 4px 0px 0px | 0 | 1 | 12px 16px 28px |
| C | providers | dark | 390 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(242, 240, 232) 4px 4px 0px 0px | 0 | 1 | 12px 16px 24px 28px |
| C | providers | dark | 320 | 0 | 0 | button-ghost sign-out 1.07 | 0 | 0 | rgb(242, 240, 232) 8px 8px 0px 0px; rgb(242, 240, 232) 4px 4px 0px 0px | 0 | 1 | 12px 16px 24px 28px |
| D | overview | light | 1440 | 0 | 0 | 0 | 0 | 34 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 36px 44px 56px 64px |
| D | overview | light | 390 | 0 | 0 | 0 | 0 | 24 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 26px 30px 40px |
| D | overview | light | 320 | 0 | 0 | 0 | 0 | 24 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 26px 30px 40px |
| D | overview | dark | 1440 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 34 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 36px 44px 56px 64px |
| D | overview | dark | 390 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 24 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 26px 30px 40px |
| D | overview | dark | 320 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 24 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 11px 12px 16px 26px 30px 40px |
| D | providers | light | 1440 | 0 | 0 | 0 | 0 | 33 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 12px 16px 36px 44px 64px |
| D | providers | light | 390 | 0 | 0 | 0 | 0 | 23 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 12px 16px 26px 30px 40px |
| D | providers | light | 320 | 0 | 0 | 0 | 0 | 23 | rgb(17, 17, 17) 10px 6px 0px 0px; rgb(17, 17, 17) 5px 3px 0px 0px | 0 | 1 | 12px 16px 26px 30px 40px |
| D | providers | dark | 1440 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 33 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(247, 246, 240) 5px 3px 0px 0px | 0 | 1 | 12px 16px 36px 44px 64px |
| D | providers | dark | 390 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 23 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(247, 246, 240) 5px 3px 0px 0px | 0 | 1 | 12px 16px 26px 30px 40px |
| D | providers | dark | 320 | 0 | 0 | button-ghost sign-out 1.11 | 0 | 23 | rgb(247, 246, 240) 10px 6px 0px 0px; rgb(247, 246, 240) 5px 3px 0px 0px | 0 | 1 | 12px 16px 26px 30px 40px |

`button-ghost sign-out` below 3:1 in dark is pre-existing. The current UI
measures 1.09 for the same element: a 1px hairline around a text-only button. It
is not introduced by any direction and is left for phase 2.

The round 2 check on the menu button bars found that **B light fails it**: B
paints the phone header in ink but leaves the bars in ink (1.0:1), so the menu
button is invisible. B's file is frozen, so it is recorded here and not fixed.
D had the same defect during round 2 and fixes it (`d-signal.css`, `.mobile-menu`).

Pre-existing in every direction and in the current UI: the phone request log
truncates the route (`openai / gpt…`) even when there is room, because
`.log-route` shares the row with the latency. That is for phase 3. The provider
square in "Needs a look" on Providers sits below its word at 390px.

## Executable guards that encode the BDR-015 contract (at `f15b350`)

Phase 1 changes none of them: the code still implements BDR-015, so changing a
guard now would fail the smoke or weaken it. "Needs" is what the new contract
requires in the phase that changes the code.

| Guard | Where | A "Loom" | B "Patchwork" | C "Ledger" | D "Signal" |
| --- | --- | --- | --- | --- | --- |
| Text contrast 4.5:1 per view/theme/width | `ui/scripts/shell-smoke.cjs:51-54`, assert `:75` | unchanged | unchanged | unchanged | unchanged |
| State colours 4.5:1 against the body | `shell-smoke.cjs:59`, assert `:76` | unchanged | unchanged | unchanged | unchanged |
| No computed shell radius above 2px | `shell-smoke.cjs:55-58`, assert `:74` | unchanged | needs an allow-list for tiles (12px), inputs (8px), pills | unchanged | needs an allow-list for pills (999px), circles (50%), the 32px slab and 20-24px boxes |
| Type sizes 11/12/16/28 (24 on a phone), 11px only in SVG | `shell-smoke.cjs:60-61`, asserts `:77-80`; per page `:1438-1441`, `:1558` | unchanged | unchanged | unchanged | needs the poster sizes 36/44/56/64px (26/30/40px on a phone) allowed, and `:79` no longer requires 28px |
| Exactly one accent surface | `shell-smoke.cjs:62`, assert `:81`; per page `:1559` | unchanged | unchanged | unchanged | unchanged |
| Active nav marker 2px, coloured, heavier weight | `shell-smoke.cjs:63-69`, asserts `:82-84` | unchanged | unchanged | unchanged | unchanged |
| Focus ring 2px solid, 2px offset, not the ink | `shell-smoke.cjs:88-95`, `:1082`, asserts `:1548-1551` | unchanged; `:90` compares against `rgb(32, 36, 43)`, which is neither the old ink `#14161a` nor the new one, so it should point at `--ink` | same | unchanged (same `:90` note) | unchanged (same `:90` note) |
| Usage error state left border `3px` | `shell-smoke.cjs:1087`, assert `:1553` | needs `6px` | needs the tile border | needs `10px` | unchanged (3px all round) |
| Empty state border `solid` | assert `shell-smoke.cjs:1556` | unchanged | unchanged | unchanged | unchanged |
| `theme-color` media queries (not values) | `shell-smoke.cjs:493`, assert `:1449` | unchanged | unchanged | unchanged | unchanged |
| a11y audit: low contrast 4.5:1 on every text node, labels, landmarks, skip link | `shell-smoke.cjs:1328-1425` (contrast `:1383-1388`), asserts `:1657-1665` | unchanged, must not be weakened | same | unchanged, must not be weakened | same |
| Reduced motion | asserts `shell-smoke.cjs:1666-1669` | unchanged | unchanged | unchanged | unchanged |
| 320px no overflow | assert `shell-smoke.cjs:1656` | unchanged | unchanged | unchanged | unchanged |
| `rg 'gradient\|backdrop-filter\|box-shadow' ui/src` = 0 | `docs/UI_STYLE.md` §10.2 (old text). Documentary, run by hand; no script runs it | rewritten in this PR to allow only `--hard`/`--hard-sm`; phase 2 should make it executable | same | same, with `--hard` 8px and `--hard-sm` 4px | same, with an asymmetric 10x6px `--hard` |
| Tokens: graphite paper/ink/surfaces, accent ink slab | `ui/src/style.css:4-63` (light), `:64-95` (dark) | phase 2 replaces values | same | phase 2 | phase 2 |
| Accent surface is a slab with a `--live` left rule | `style.css:579`, `:706` | phase 2: outline + `--hard` | tile | vermilion slab, 4px outline, `--hard` | pink slab, 32px radius, `--hard` |
| Active nav: 1px/2px rule, no fill | `style.css:122`, `:707` | phase 2: highlight block | pill | inverted black block | yellow pill |
| Buttons 1px border, press 1px | `style.css:136-143` | phase 2: 2px outline, `--hard-sm`, press 2px | pills | 3px box, mono capitals, press 4px | 3px pill, press 5x3px |
| Empty and error states | `style.css:650`, `:655` | phase 2 | phase 2 | phase 2 | phase 2 |
| Section rules 2px, share bar 2px | `style.css:131`, `:603`, `:740`, `:746`, `:760`, `:764` | phase 3/4: 3px rules, 6px bars | tiles | 4px rules, every table cell ruled | 6px rules, open tables |
| Page title weight | `style.css:133`, `:587`, `:705` (300 wins) | phase 2: 700 | 700 | 700 capitals | 700 at 64px |
| `theme-color` values | `ui/index.html:6-7` | phase 4: `#ecebe4` / `#111210` | phase 4 | phase 4: `#efeee9` / `#0b0b0b` | phase 4: `#f7f6f0` / `#111111` |
| Favicon colours | `ui/public/favicon.svg:6-10` | unchanged (ink and cyan stay) | phase 4 | unchanged | phase 4 |

There is **no executable 3:1 control-boundary assertion** in `shell-smoke.cjs`.
It asserts 4.5:1 for text and state colours only. `capture.cjs` measures 3:1
for the preview. Phase 2 should add the same check to the smoke; it adds a
guard and weakens none.

C and D add no guard family beyond this list. They add one rule the phase that
implements them must enforce: on a phone the menu button's bars need 3:1 against
the header, because a coloured or inverted header can hide them (B already fails
this). `capture.cjs` measures it; `shell-smoke.cjs` should get the same check
when the header colour changes.
