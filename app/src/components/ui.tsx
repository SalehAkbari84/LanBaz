/**
 * Small, dependency-free UI primitives shared by every page and the overlay.
 * Styling lives in Tailwind classes and the component layer in index.css.
 */

import { AnimatePresence, motion } from 'framer-motion'
import { Check, Copy, X } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'

import { useNumber, useT } from '../i18n'
import type { PeerState } from '../types/protocol'

export function Card({ className = '', children, dropRoom }: { className?: string; children: ReactNode; dropRoom?: string }) {
  return (
    <section className={`card ${className}`} data-drop-room={dropRoom}>
      {children}
    </section>
  )
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <header className="flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-white">{title}</h1>
        {subtitle ? <p className="mt-1 max-w-2xl text-sm text-slate-400">{subtitle}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  )
}

export function SectionTitle({ icon, children, extra }: { icon?: ReactNode; children: ReactNode; extra?: ReactNode }) {
  return (
    <div className="mb-3 flex items-center justify-between gap-3">
      <h2 className="flex items-center gap-2 text-sm font-semibold text-slate-100">
        {icon ? <span className="text-accent">{icon}</span> : null}
        {children}
      </h2>
      {extra}
    </div>
  )
}

export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  label?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative h-6 w-11 shrink-0 rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-40 ${
        checked ? 'border-transparent bg-accent-gradient' : 'border-white/10 bg-white/[0.06]'
      }`}
    >
      <motion.span
        layout
        transition={{ type: 'spring', stiffness: 500, damping: 32 }}
        className="absolute top-0.5 h-[18px] w-[18px] rounded-full bg-white shadow"
        style={{ insetInlineStart: checked ? 22 : 3 }}
      />
    </button>
  )
}

export function Row({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-6 py-3">
      <div className="min-w-0">
        <div className="text-sm text-slate-100">{title}</div>
        {hint ? <div className="mt-0.5 text-xs text-slate-500">{hint}</div> : null}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  )
}

export function Segmented<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T
  options: Array<{ value: T; label: string }>
  onChange: (v: T) => void
}) {
  return (
    <div className="inline-flex w-fit rounded-xl border border-white/10 bg-white/[0.03] p-1">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          onClick={() => onChange(o.value)}
          className={`relative rounded-lg px-3 py-1.5 text-xs font-medium transition-colors ${
            value === o.value ? 'text-white' : 'text-slate-400 hover:text-slate-200'
          }`}
        >
          {value === o.value ? (
            <motion.span layoutId={`seg-${options.map((x) => x.value).join()}`} className="absolute inset-0 rounded-lg bg-accent/25" />
          ) : null}
          <span className="relative">{o.label}</span>
        </button>
      ))}
    </div>
  )
}

/** Deterministic gradient avatar with initials. */
export function Avatar({ name, size = 36, ring }: { name: string; size?: number; ring?: string }) {
  const initials = initialsOf(name)
  const hue = hashHue(name)
  return (
    <div
      className={`grid shrink-0 place-items-center rounded-full font-semibold text-white ${ring ?? ''}`}
      style={{
        width: size,
        height: size,
        fontSize: size * 0.38,
        background: `linear-gradient(135deg, hsl(${hue} 80% 60%), hsl(${(hue + 50) % 360} 80% 45%))`,
      }}
      aria-hidden="true"
    >
      {initials}
    </div>
  )
}

function initialsOf(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  const first = Array.from(parts[0] ?? '')
  if (first.length === 0) return '?'
  if (parts.length === 1) return first.slice(0, 2).join('').toUpperCase()
  const second = Array.from(parts[1] ?? '')
  return ((first[0] ?? '') + (second[0] ?? '')).toUpperCase()
}

function hashHue(s: string): number {
  let h = 0
  for (const ch of s) h = (h * 31 + ch.codePointAt(0)!) >>> 0
  return h % 360
}

/** Signal-strength style ping indicator. */
export function PingBadge({ ms, measured = true }: { ms: number; measured?: boolean }) {
  const t = useT()
  const num = useNumber()
  if (!measured || ms <= 0) return <span className="text-xs text-slate-600">—</span>
  const level = ms < 40 ? 4 : ms < 80 ? 3 : ms < 150 ? 2 : 1
  const color = level >= 3 ? 'bg-status-running' : level === 2 ? 'bg-status-stopping' : 'bg-status-error'
  return (
    <span className="inline-flex items-center gap-1.5" title={t('common.ms', { n: num(ms) })}>
      <span className="flex items-end gap-[2px]" aria-hidden="true">
        {[1, 2, 3, 4].map((b) => (
          <span key={b} className={`w-[3px] rounded-sm ${b <= level ? color : 'bg-white/15'}`} style={{ height: 3 + b * 3 }} />
        ))}
      </span>
      <span className="ltr text-xs tabular-nums text-slate-300">{num(Math.round(ms))}ms</span>
    </span>
  )
}

export function StateDot({ state }: { state: PeerState }) {
  const t = useT()
  const good = state === 'active' || state === 'network_ready' || state === 'degraded'
  const bad = state === 'failed' || state === 'disconnected'
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-slate-400">
      <span
        className={`h-2 w-2 rounded-full ${
          good ? (state === 'degraded' ? 'bg-status-stopping' : 'bg-status-running') : bad ? 'bg-status-error' : 'animate-pulse-dot bg-accent'
        }`}
      />
      {t(`state.${state}` as never)}
    </span>
  )
}

export function EmptyState({ icon, title, hint, children }: { icon: ReactNode; title: string; hint?: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-12 text-center">
      <div className="grid h-14 w-14 place-items-center rounded-2xl bg-accent/15 text-accent">{icon}</div>
      <div className="text-base font-medium text-slate-100">{title}</div>
      {hint ? <p className="max-w-md text-sm text-slate-400">{hint}</p> : null}
      {children ? <div className="mt-2 flex flex-wrap justify-center gap-2">{children}</div> : null}
    </div>
  )
}

export function Banner({ tone = 'info', children, onClose }: { tone?: 'info' | 'warn' | 'error' | 'ok'; children: ReactNode; onClose?: () => void }) {
  const tones = {
    info: 'border-accent/30 bg-accent/10 text-slate-200',
    warn: 'border-amber-400/30 bg-amber-400/10 text-amber-100',
    error: 'border-red-400/30 bg-red-500/10 text-red-100',
    ok: 'border-emerald-400/30 bg-emerald-400/10 text-emerald-100',
  }
  return (
    <div className={`flex items-start justify-between gap-3 rounded-xl border px-4 py-3 text-sm ${tones[tone]}`} role={tone === 'error' ? 'alert' : 'status'}>
      <div className="min-w-0 break-words">{children}</div>
      {onClose ? (
        <button type="button" className="shrink-0 rounded-md p-1 text-current/70 hover:bg-white/10" onClick={onClose} aria-label="close">
          <X size={14} />
        </button>
      ) : null}
    </div>
  )
}

/** Copy-to-clipboard button with a brief confirmation. */
export function CopyButton({
  value,
  label,
  className = 'btn btn-sm',
  iconOnly = false,
}: {
  value: string
  label?: string
  className?: string
  iconOnly?: boolean
}) {
  const t = useT()
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  const copy = async () => {
    if (!(await writeClipboard(value))) return
    setCopied(true)
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setCopied(false), 1600)
  }
  return (
    <button type="button" className={className} onClick={() => void copy()} title={label ?? t('common.copy')}>
      {copied ? <Check size={14} className="text-status-running" /> : <Copy size={14} />}
      {iconOnly ? null : <span>{copied ? t('common.copied') : label ?? t('common.copy')}</span>}
    </button>
  )
}

export async function writeClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    try {
      const area = document.createElement('textarea')
      area.value = text
      area.style.position = 'fixed'
      area.style.opacity = '0'
      document.body.appendChild(area)
      area.select()
      const ok = document.execCommand('copy')
      area.remove()
      return ok
    } catch {
      return false
    }
  }
}

/** Centered modal with backdrop. */
export function Modal({ open, onClose, children }: { open: boolean; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  return (
    <AnimatePresence>
      {open ? (
        <motion.div
          className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-6 backdrop-blur-sm"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          onClick={onClose}
        >
          <motion.div
            className="glass w-full max-w-md bg-surface-raised/95 p-6"
            initial={{ opacity: 0, y: 16, scale: 0.97 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 8, scale: 0.98 }}
            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
            onClick={(e) => e.stopPropagation()}
          >
            {children}
          </motion.div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}

/** Re-renders on an interval so countdowns stay honest. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

/** A tiny line chart of recent round trips; the dashed line is a game's limit. */
export function Sparkline({ values, limit, width = 72, height = 22 }: { values: number[]; limit?: number; width?: number; height?: number }) {
  if (values.length < 2) return <span style={{ width, height }} className="inline-block" />
  const top = Math.max(...values, limit ?? 0, 20) * 1.15
  const x = (i: number) => (i / (values.length - 1)) * width
  const y = (v: number) => height - (v / top) * height
  const d = values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
  const last = values[values.length - 1] ?? 0
  const color = limit && last > limit ? 'stroke-status-error' : last < 80 ? 'stroke-status-running' : last < 150 ? 'stroke-status-stopping' : 'stroke-status-error'
  return (
    <svg width={width} height={height} className="ltr shrink-0 overflow-visible" aria-hidden="true">
      {limit ? <line x1={0} x2={width} y1={y(limit)} y2={y(limit)} className="stroke-white/25" strokeDasharray="2 2" strokeWidth={1} /> : null}
      <path d={d} fill="none" className={color} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}
