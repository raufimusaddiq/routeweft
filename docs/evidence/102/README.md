# #102 design round — three directions

Screenshots are of the real `routeweft serve` binary with a locally seeded database (9,246 usage events over 30 days,
15 connections on 13 providers, 4 API keys). The seed is not committed. Each direction is a prototype of the Overview,
Usage and Providers pages only; production code in `ui/src` is unchanged. `ui/src/main.tsx` is untouched in all three.

Files: `{a,b,c}/{overview,usage,providers}-{light,dark}-{1440,390}.png` (1440x900 and 390x844, full page).
Prototype sources: `prototypes/`.

| | Direction | Structure |
|---|---|---|
| A | Dense instrument | readout strip, tables, inline row expansion |
| B | Editorial | headline sentence, captioned figures, list + sheet |
| C | Spatial | sticky live strip, tonal panels and wells, master/detail |

Type scale 28 / 16 / 12 / 10 in all three. No gradient, backdrop-filter or box-shadow. Radius 0 (panels), 2px (buttons, chips).
Automated check: no text below 4.5:1 on any of the 36 screenshots' pages.
