# #263 phase 1 preview: retro neo-brutalist directions

**Preview only.** Nothing here ships: no file in `ui/src` imports this directory,
`tsconfig.json` includes only `src`, and `vite build` starts from `index.html`.
Each direction is an overlay stylesheet that `capture.cjs` injects over the
built `style.css` in a browser, so the screenshots show the real Overview and
Providers markup, the real nav and the real drawer, only restyled. The
screenshots carry a magenta "PREVIEW #263" tag.

| Direction | File | In one line |
| --- | --- | --- |
| A "Loom" (recommended) | `a-loom.css` | Square 0/2px geometry kept. Linen paper, ink outline plus a hard 4px shadow on the boxes that mean something, a lime highlighter under the headline numbers and on the active rail item, a cyan-tinted live card. |
| B "Patchwork" | `b-patchwork.css` | Rounded 12px tiles around every figure, pill buttons and chips, an ink-filled rail, sage paper, an orange highlighter block, mint and sky pastel patches. |

Why A: it keeps what is Routeweft's own (square instrument geometry, the
editorial rules, the weave mark, mono figures), so it reads as a sibling of
Richmod and Antares rather than a third rounded product. It also keeps the
radius guard, the type scale and the nav marker guard unchanged. B is the louder
option. It needs the radius guard rewritten, and on a phone its tile padding
leaves less room for the provider rows.

## Run

```bash
cd ui
npm ci && npm install --no-save --no-package-lock playwright-core@1.49.1
npx playwright-core install chromium
npm run build
setsid npx vite preview --host 127.0.0.1 --port 4173 & echo $!   # stop it by PID afterwards
ROUTEWEFT_SMOKE_CHROME=<chromium> node preview/263/capture.cjs /tmp/routeweft-263-preview [current|a|b]
```

The API is stubbed with sanitized fixtures (no server, no credentials, no
production data). The page clock is frozen at 2026-09-25T12:00Z.

## Screenshots

`docs/evidence/263/preview/`, per direction (`a-`, `b-`):

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
For every row, A and B: no browser errors, Escape closes the drawer and returns
focus to `mobile-menu`, and the provider selection works.

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

`button-ghost sign-out` below 3:1 in dark is pre-existing. The current UI
measures 1.09 for the same element: a 1px hairline around a text-only button. It
is not introduced by either direction and is left for phase 2.

## Executable guards that encode the BDR-015 contract (at `f15b350`)

Phase 1 changes none of them: the code still implements BDR-015, so changing a
guard now would fail the smoke or weaken it. "Needs" is what the new contract
requires in the phase that changes the code.

| Guard | Where | A "Loom" | B "Patchwork" |
| --- | --- | --- | --- |
| Text contrast 4.5:1 per view/theme/width | `ui/scripts/shell-smoke.cjs:51-54`, assert `:75` | unchanged | unchanged |
| State colours 4.5:1 against the body | `shell-smoke.cjs:59`, assert `:76` | unchanged | unchanged |
| No computed shell radius above 2px | `shell-smoke.cjs:55-58`, assert `:74` | unchanged | needs an allow-list for tiles (12px), inputs (8px), pills |
| Type sizes 11/12/16/28 (24 on a phone), 11px only in SVG | `shell-smoke.cjs:60-61`, asserts `:77-80`; per page `:1438-1441`, `:1558` | unchanged | unchanged |
| Exactly one accent surface | `shell-smoke.cjs:62`, assert `:81`; per page `:1559` | unchanged | unchanged |
| Active nav marker 2px, coloured, heavier weight | `shell-smoke.cjs:63-69`, asserts `:82-84` | unchanged | unchanged |
| Focus ring 2px solid, 2px offset, not the ink | `shell-smoke.cjs:88-95`, `:1082`, asserts `:1548-1551` | unchanged; `:90` compares against `rgb(32, 36, 43)`, which is neither the old ink `#14161a` nor the new one, so it should point at `--ink` | same |
| Usage error state left border `3px` | `shell-smoke.cjs:1087`, assert `:1553` | needs `6px` | needs the tile border |
| Empty state border `solid` | assert `shell-smoke.cjs:1556` | unchanged | unchanged |
| `theme-color` media queries (not values) | `shell-smoke.cjs:493`, assert `:1449` | unchanged | unchanged |
| a11y audit: low contrast 4.5:1 on every text node, labels, landmarks, skip link | `shell-smoke.cjs:1328-1425` (contrast `:1383-1388`), asserts `:1657-1665` | unchanged, must not be weakened | same |
| Reduced motion | asserts `shell-smoke.cjs:1666-1669` | unchanged | unchanged |
| 320px no overflow | assert `shell-smoke.cjs:1656` | unchanged | unchanged |
| `rg 'gradient|backdrop-filter|box-shadow' ui/src` = 0 | `docs/UI_STYLE.md` §10.2 (old text). Documentary, run by hand; no script runs it | rewritten in this PR to allow only `--hard`/`--hard-sm`; phase 2 should make it executable | same |
| Tokens: graphite paper/ink/surfaces, accent ink slab | `ui/src/style.css:4-63` (light), `:64-95` (dark) | phase 2 replaces values | same |
| Accent surface is a slab with a `--live` left rule | `style.css:579`, `:706` | phase 2: outline + `--hard` | tile |
| Active nav: 1px/2px rule, no fill | `style.css:122`, `:707` | phase 2: highlight block | pill |
| Buttons 1px border, press 1px | `style.css:136-143` | phase 2: 2px outline, `--hard-sm`, press 2px | pills |
| Empty and error states | `style.css:650`, `:655` | phase 2 | phase 2 |
| Section rules 2px, share bar 2px | `style.css:131`, `:603`, `:740`, `:746`, `:760`, `:764` | phase 3/4: 3px rules, 6px bars | tiles |
| Page title weight | `style.css:133`, `:587`, `:705` (300 wins) | phase 2: 700 | 700 |
| `theme-color` values | `ui/index.html:6-7` | phase 4: `#ecebe4` / `#111210` | phase 4 |
| Favicon colours | `ui/public/favicon.svg:6-10` | unchanged (ink and cyan stay) | phase 4 |

There is **no executable 3:1 control-boundary assertion** in `shell-smoke.cjs`.
It asserts 4.5:1 for text and state colours only. `capture.cjs` measures 3:1
for the preview. Phase 2 should add the same check to the smoke; it adds a
guard and weakens none.
