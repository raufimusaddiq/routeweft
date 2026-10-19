# #105 — three candidate marks for Routeweft

Drawing only. Nothing here is wired into the app, the favicon or `ui/index.html`, and no candidate is chosen.

Each candidate is one hand-written SVG (`candidate-x/mark.svg`, 32 x 32 viewBox, no letters, no image asset, no icon font,
no gradient, no shadow, no filter, square corners only). Colour comes from `currentColor` for the main form and
`var(--mark-accent, currentColor)` for one thread, so the mark inherits the ink of its parent, takes one accent from a CSS
variable, and falls back to a single colour (monochrome) when the variable is not set. Light and dark need no separate asset.

Renders: `candidate-x/x-{16,32,180,512}-{light,dark}.png`, 24 in all, on the UI grounds (light `#f4f4f2` with ink `#14161a`
and accent `#00727f`; dark `#0c0e11` with ink `#ecedef` and accent `#4fd1e0`). The 16px renders are 16 x 16 real pixels.

## A. Plain weave

**Idea:** the weft, literally: three warp threads and three weft threads, each going over, under, over the ones that cross it.

**Where the form came from:** the plain-weave interlace, the simplest real woven structure. The thread underneath is cut two
units either side of the thread on top, so over and under read without any outline. Every edge sits on an even unit, so at
16px the threads land on whole pixels. Warp is the ink, weft is the accent.

**Reads at 16px:** yes, the clearest of the three. **Monochrome:** yes, the cuts carry the weave. **Risk:** a woven square is a
familiar sign; the distinct part is the cut-and-gap drawing, which only shows from about 32px.

## B. Routes drawn together

**Idea:** a divergence that re-joins: three routes come in, are drawn into one tight bundle, and fan out again, with one weft bar
tying the bundle at its narrowest point.

**Where the form came from:** a cable loom, where parallel conductors are gathered and bound by a crossing thread. Three lanes
with 2-unit gaps; the outer two step in at 45 degrees and out again; the weft is a single vertical bar through the middle.

**Reads at 16px:** as a symmetrical star with a stem, with the bundle only visible from 32px. **Monochrome:** yes, though the bar
and the middle lane fuse. **Risk:** at small sizes it resembles an asterisk; the idea lives at 32px and above.

## C. Two threads braided

**Idea:** a route that diverges and re-joins as two threads that cross, spread apart and cross again, so every crossing is a weave.

**Where the form came from:** a two-strand braid. The first crossing has the accent thread over the ink thread, the second has
the ink thread over the accent thread; the thread underneath is cut 3.3 units along its own line either side of the other
(1.5 units clear of the thread on top). Straight segments only, no curves.

**Reads at 16px:** as a small braid or zigzag; the diagonals soften at 16px but the crossing stays visible. **Monochrome:** yes,
the two over-under gaps carry it. **Risk:** the closest of the three to a generic "X" or arrow pair at a glance.
