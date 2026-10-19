# #102 design round — three directions, Space Grotesk + JetBrains Mono

Screenshots are of the real `routeweft serve` binary with a locally seeded database (9,246 usage events over 30 days,
15 connections on 13 providers, 4 API keys). The seed is not committed. Each direction prototypes Overview, Usage and
Providers only; `ui/src/main.tsx` is untouched in all three.

Files: `{a,b,c}/{overview,usage,providers}-{light,dark}-{1440,390}.png` (1440x900 and 390x844, full page, 36 files).
Prototype sources: `prototypes/` (page components, `direction.css`, shared helpers).

Type: Space Grotesk for headings, body and large figures; JetBrains Mono for labels, tables and technical values.
Both are self-hosted from `ui/public/fonts/` (latin subset, variable woff2, `font-display: swap`). Scale 28 / 16 / 12 / 10.
Delta arrows are `↑ ↓` (inside the latin subset) instead of `▲ ▼`.

| | Direction | Structure | Pushed further this round |
|---|---|---|---|
| A | Dense instrument | readout strip, tables, inline row expansion | tick ruler on the strip rule; red rule on the Connections tile when an account is disabled |
| B | Editorial | headline sentence, captioned figures, list + sheet | weekend bands on the daily chart, with the caption saying so |
| C | Spatial | sticky live strip, tonal panels and wells | 1px lit top edge on each layer; hero facts row under the chart |

Checked on every page: no text below 4.5:1 and no horizontal overflow at 390.
