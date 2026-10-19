import type { ReactNode } from 'react'

export type Tone = 'good' | 'warn' | 'bad' | 'idle'

export function Dot({ tone }: { tone: Tone }) {
  return <i className={`dot dot-${tone}`} aria-hidden="true" />
}

// Delta against a stated basis. The arrow and signed number carry direction, tone carries judgement.
export function Delta({ value, basis, good = 'up', digits = 1, suffix = '%' }: { value: number | null; basis: string; good?: 'up' | 'down' | 'none'; digits?: number; suffix?: string }) {
  if (value === null || !Number.isFinite(value)) return <span className="delta delta-none">{basis}</span>
  const up = value >= 0
  const tone = good === 'none' || Math.abs(value) < 0.05 ? 'idle' : (up === (good === 'up') ? 'good' : 'bad')
  return <span className={`delta delta-${tone}`}><span aria-hidden="true">{up ? '↑' : '↓'}</span> {up ? '+' : '−'}{Math.abs(value).toFixed(digits)}{suffix} <span className="delta-basis">{basis}</span></span>
}

export function Status({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className="status"><Dot tone={tone} />{children}</span>
}

export function Tile({ label, value, unit, children, accent, className = '' }: { label: string; value: ReactNode; unit?: string; children?: ReactNode; accent?: boolean; className?: string }) {
  return <section className={`tile${accent ? ' accent-surface' : ''} ${className}`.trim()} aria-label={label}>
    <h3 className="tile-label">{label}</h3>
    <p className="tile-value"><span className="tile-number">{value}</span>{unit && <span className="tile-unit">{unit}</span>}</p>
    {children}
  </section>
}
