// Compile-time guard, checked by `npm run typecheck` and never imported by the app.
// Every comparison takes an `Observed` (built only by observeSeries). Handing one a raw, possibly zero-filled
// series must not compile. If a `@ts-expect-error` below stops erroring, a comparison has started accepting raw days.
import { observedAverage, sameDayLastWeek, weekOverWeek, weekendLighter, type Day } from './format'

declare const raw: Day[]

// @ts-expect-error a raw series is not an Observed
observedAverage(raw)
// @ts-expect-error a raw series is not an Observed
weekOverWeek(raw)
// @ts-expect-error a raw series is not an Observed
weekendLighter(raw)
// @ts-expect-error a raw series is not an Observed
sameDayLastWeek(raw)

// @ts-expect-error an object literal cannot forge an Observed
observedAverage({ drawn: raw, days: raw, firstRecorded: null })
