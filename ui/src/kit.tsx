import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from 'react'

import { niceStep } from './chartMath'
import type { Tone } from './format'

const numberFormat = new Intl.NumberFormat()

export type ChartPoint = { label: string; short: string; value: number }

function useWidth(fallback: number) {
  const ref = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(fallback)
  useEffect(() => {
    const node = ref.current
    if (!node) return
    setWidth(Math.max(240, Math.floor(node.clientWidth)))
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(entries => {
      const next = Math.floor(entries[0].contentRect.width)
      if (next > 0) setWidth(Math.max(240, next))
    })
    observer.observe(node)
    return () => observer.disconnect()
  }, [fallback])
  return [ref, width] as const
}

// Bar chart with axis, gridlines, peak label and a hover/keyboard readout. Plain SVG, no dependencies.
export function BarChart({ points, unit, title, shade, current, ghost, reference }: { points: ChartPoint[]; unit: string; title: string; shade?: (label: string) => boolean; current?: (index: number) => boolean; ghost?: (index: number) => number | null; reference?: { value: number; label: string } }) {
  const [ref, width] = useWidth(640)
  const [active, setActive] = useState<number | null>(null)
  if (points.length === 0) return null
  const height = 200
  const margin = { top: 20, right: 8, bottom: 24, left: 44 }
  const innerWidth = Math.max(1, width - margin.left - margin.right)
  const innerHeight = height - margin.top - margin.bottom
  const max = Math.max(1, ...points.map(point => point.value), ...(ghost ? points.map((_, index) => ghost(index) ?? 0) : []))
  const step = niceStep(max / 3)
  const top = Math.ceil(max / step) * step
  const ticks: number[] = []
  for (let value = 0; value <= top; value += step) ticks.push(value)
  const band = innerWidth / Math.max(1, points.length)
  const barWidth = Math.max(2, Math.min(28, band * 0.64))
  const labelEvery = Math.max(1, Math.ceil(46 / band))
  const peak = points.reduce((best, point, index) => (point.value > points[best].value ? index : best), 0)
  const y = (value: number) => margin.top + innerHeight - (value / top) * innerHeight
  const x = (index: number) => margin.left + band * index + band / 2
  const shown = active ?? peak

  const onPointer = (event: PointerEvent<SVGSVGElement>) => {
    const box = event.currentTarget.getBoundingClientRect()
    const index = Math.floor((event.clientX - box.left - margin.left) / band)
    setActive(index >= 0 && index < points.length ? index : null)
  }
  const onKey = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'ArrowRight') { event.preventDefault(); setActive(current => Math.min(points.length - 1, (current ?? peak - 1) + 1)) }
    else if (event.key === 'ArrowLeft') { event.preventDefault(); setActive(current => Math.max(0, (current ?? peak + 1) - 1)) }
    else if (event.key === 'Escape') setActive(null)
  }

  return <figure className="chart usage-chart" ref={ref}>
    <div className="chart-readout" aria-live="polite" aria-atomic="true">
      <span>{active === null ? 'Peak' : points[shown].label}</span>
      <strong>{numberFormat.format(points[shown].value)} {unit}</strong>
      {active === null && <span>{points[peak].label}</span>}
      {active !== null && ghost && ghost(active) ? <span className="chart-vs">{points[active].value >= (ghost(active) as number) ? '↑' : '↓'} {Math.abs(((points[active].value - (ghost(active) as number)) / (ghost(active) as number)) * 100).toFixed(1)}% vs 7 days before ({numberFormat.format(ghost(active) as number)})</span> : null}
    </div>
    <div className="chart-focus" tabIndex={0} role="group" aria-label={`${title}. Use left and right arrow keys to read each day.`} onKeyDown={onKey} onBlur={() => setActive(null)}>
      <svg className="chart-svg" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true" onPointerMove={onPointer} onPointerLeave={() => setActive(null)}>
        <g className="chart-grid">
          {ticks.map(tick => <g key={tick}>
            <line className={tick === 0 ? 'chart-baseline' : undefined} x1={margin.left} x2={width - margin.right} y1={y(tick)} y2={y(tick)} />
            <text className="chart-tick" x={margin.left - 8} y={y(tick) + 3} textAnchor="end">{numberFormat.format(tick)}</text>
          </g>)}
        </g>
        {shade && points.map((point, index) => shade(point.label) && <rect key={`s${point.label}`} className="chart-band" x={x(index) - band / 2} y={margin.top} width={band} height={innerHeight} />)}
        {active !== null && <line className="chart-cursor" x1={x(active)} x2={x(active)} y1={margin.top} y2={margin.top + innerHeight} />}
        {points.map((point, index) => {
          const barHeight = Math.max(point.value > 0 ? 2 : 0, (point.value / top) * innerHeight)
          return <rect key={point.label} className={`usage-bar${current?.(index) ? ' usage-bar-cur' : ''}${index === (active ?? peak) ? ' usage-bar-active' : ''}`} x={x(index) - barWidth / 2} y={margin.top + innerHeight - barHeight} width={barWidth} height={barHeight} />
        })}
        {ghost && points.map((point, index) => { const g = ghost(index); if (g === null) return null; return <line key={`g${point.label}`} className="chart-ghost" x1={x(index) - barWidth / 2} x2={x(index) + barWidth / 2} y1={y(g)} y2={y(g)} /> })}
        {reference && <line className="chart-ref" x1={margin.left} x2={width - margin.right} y1={y(reference.value)} y2={y(reference.value)} />}
        {active === null && <text className="chart-peak" x={x(peak)} y={y(points[peak].value) - 6} textAnchor="middle">{numberFormat.format(points[peak].value)}</text>}
        {points.map((point, index) => index % labelEvery === 0 && <text key={point.label} className="chart-tick" x={x(index)} y={height - 6} textAnchor="middle">{point.short}</text>)}
      </svg>
    </div>
    <table className="sr-only">
      <caption>{title}</caption>
      <thead><tr><th>Day</th><th>{unit}</th></tr></thead>
      <tbody>{points.map(point => <tr key={point.label}><td>{point.label}</td><td className="num">{numberFormat.format(point.value)}</td></tr>)}</tbody>
    </table>
  </figure>
}

// Trend line for a metric card. Needs two or more points; otherwise it renders nothing rather than a fake line.
export function Sparkline({ values, label, caption }: { values: number[]; label: string; caption?: string }) {
  if (values.length < 2) return null
  const min = Math.min(...values)
  const range = Math.max(...values) - min
  const point = (value: number, index: number) => [(index / (values.length - 1)) * 100, range === 0 ? 12 : 21 - ((value - min) / range) * 18] as const
  const path = values.map((value, index) => point(value, index)).map(([px, py], index) => `${index === 0 ? 'M' : 'L'}${px.toFixed(2)} ${py.toFixed(2)}`).join(' ')
  const [lastX, lastY] = point(values[values.length - 1], values.length - 1)
  return <div className="sparkline">
    <svg viewBox="0 0 100 24" preserveAspectRatio="none" aria-hidden="true"><path d={path} vectorEffect="non-scaling-stroke" /></svg>
    <span className="sparkline-end" style={{ left: `${lastX}%`, top: `${(lastY / 24) * 100}%` }} aria-hidden="true" />
    {caption && <span className="sparkline-caption">{caption}</span>}
    <span className="sr-only">{label} trend: {values.map(value => numberFormat.format(value)).join(', ')}</span>
  </div>
}

export type Segment = { key: string; label: string; value: number; tone: Tone }

// Horizontal stacked bar plus a legend whose rows carry the label and count, so colour is never the only signal.
export function StackedBar({ segments, title }: { segments: Segment[]; title: string }) {
  const total = segments.reduce((sum, segment) => sum + segment.value, 0)
  if (total <= 0) return null
  return <div className="stacked">
    <div className="stacked-bar" aria-hidden="true">{segments.filter(segment => segment.value > 0).map(segment => <span key={segment.key} className={`stacked-${segment.tone}`} style={{ flexGrow: segment.value }} />)}</div>
    <ul className="stacked-legend" aria-label={title}>{segments.map(segment => <li key={segment.key} className="breakdown-row">
      <span className={`stacked-swatch stacked-${segment.tone}`} aria-hidden="true" />
      <code>{segment.label}</code>
      <span className="breakdown-count">{numberFormat.format(segment.value)}</span>
      <span className="count-label">{Math.round((segment.value / total) * 1000) / 10}%</span>
    </li>)}</ul>
  </div>
}

export function Skeleton({ width, height }: { width?: string; height?: number }) {
  return <span className="skeleton" style={{ width, height }} aria-hidden="true" />
}

export function SkeletonRows({ rows, columns = 4 }: { rows: number; columns?: number }) {
  return <div className="skeleton-rows" aria-hidden="true">{Array.from({ length: rows }, (_, row) => <div className="skeleton-row" key={row}>{Array.from({ length: columns }, (_, column) => <Skeleton key={column} width={column === 0 ? '18%' : `${12 + ((row * 7 + column * 11) % 18)}%`} />)}</div>)}</div>
}

// Loading shape of an editorial page: the lead (headline and its line), a figure, and table rows.
export function EditorialSkeleton() {
  return <div className="skeleton-page" aria-hidden="true">
    <div className="ed-row gap-s"><div className="ed-main"><Skeleton width="62%" height={28} /><Skeleton width="48%" height={16} /></div><div className="ed-margin"><Skeleton width="100%" height={112} /></div></div>
    <div className="ed-row gap-s"><div className="ed-main"><Skeleton width="30%" height={16} /><Skeleton width="100%" height={200} /></div><div className="ed-margin"><Skeleton width="100%" height={160} /></div></div>
    <div className="ed-row gap-s"><div className="ed-main"><Skeleton width="30%" height={16} /><SkeletonRows rows={6} columns={4} /></div></div>
  </div>
}

export function EmptyState({ reason, children }: { reason: string; children?: ReactNode }) {
  return <div className="empty-state"><p>{reason}</p>{children}</div>
}

export function ErrorNotice({ message, onRetry, busy, className = '' }: { message: string; onRetry: () => void; busy?: boolean; className?: string }) {
  return <section className={`page-error ${className}`.trim()} role="alert"><p>{message}</p><button className="button-secondary" type="button" disabled={busy} onClick={onRetry}>Retry</button></section>
}
