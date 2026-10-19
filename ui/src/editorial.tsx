import type { ReactNode } from 'react'
import { pct, rate } from './format'

// Building blocks of the editorial layout: a main column, a 300px margin column beside it, captions under figures.
export type Tone = 'good' | 'warn' | 'bad' | 'idle'

export function Dot({ tone }: { tone: Tone }) {
  return <i className={`dot dot-${tone}`} aria-hidden="true" />
}

// Status is never the colour alone: the dot always sits beside the word.
export function Status({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className="status"><Dot tone={tone} />{children}</span>
}

// gap is the space above the section: s = 40px (same thought), m = 72px (new topic), l = 120px (new chapter).
export function Row({ gap = 'm', label, children }: { gap?: 's' | 'm' | 'l'; label: string; children: ReactNode }) {
  return <section className={`ed-row gap-${gap}`} aria-label={label}>{children}</section>
}
export function Main({ title, children }: { title: string; children: ReactNode }) {
  return <div className="ed-main"><h2 className="ed-h2">{title}</h2>{children}</div>
}
export function Caption({ kicker, children }: { kicker: string; children: ReactNode }) {
  return <p className="ed-caption"><span className="ed-kicker">{kicker}</span> {children}</p>
}
export function Margin({ children }: { children: ReactNode }) { return <aside className="ed-margin">{children}</aside> }
export function Fact({ label, value, note }: { label: string; value: ReactNode; note?: ReactNode }) {
  return <div className="ed-fact"><dt>{label}</dt><dd>{value}</dd>{note && <dd className="ed-fact-note">{note}</dd>}</div>
}

// A second voice at headline scale: one sentence drawn from the data, with the action it drives.
// A third statement (the busiest hour) belongs after this one once the API serves hourly buckets: see issue #104.
export function Statement({ children, action, href }: { children: ReactNode; action?: string; href?: string }) {
  return <section className="ed-statement-row gap-m" aria-label="Finding"><p className="ed-statement">{children}</p>{action && <a className="ed-link-out" href={href}>{action}</a>}</section>
}

// Error rate with its own bar (scaled to 10%); red only when the provider is clearly above the fleet rate.
export function ErrCell({ errors, requests, bad }: { errors: number; requests: number; bad: boolean }) {
  const value = rate(errors, requests)
  return <td className="num err-cell"><span className={`err-bar${bad ? ' err-bad' : ''}`} aria-hidden="true"><span style={{ width: `${Math.min(100, (value / 10) * 100)}%` }} /></span><span className={bad ? 'err-text-bad' : ''}>{pct(value)}</span></td>
}
