import { Copy, FileArchive, Pause, Play, Terminal, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'

import { Card, SectionTitle, writeClipboard } from '../../components/ui'
import { useT } from '../../i18n'
import { invoke } from '@tauri-apps/api/core'
import { isTauri } from '../../services/tauri'
import { toast } from '../../stores/toasts'
import { daemonClient, useDaemonStore } from '../../stores/daemon'

interface LogEntry {
  seq: number
  time: string
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
  msg: string
  attrs?: string
}

const MAX = 3000

// Messages that report something that worked, shown green.
const GOOD = /established|connected|ready|attached|accepted|removed a leftover|added|opened|started|written/i

function tone(e: LogEntry): string {
  if (e.level === 'ERROR') return 'text-red-400'
  if (e.level === 'WARN') return 'text-amber-300'
  if (GOOD.test(e.msg)) return 'text-emerald-400'
  if (e.level === 'DEBUG') return 'text-slate-500'
  return 'text-slate-300'
}

function line(e: LogEntry): string {
  return `${e.time} ${e.level.padEnd(5)} ${e.msg}${e.attrs ? ' ' + e.attrs : ''}`
}

/** Settings → Developer: the engine's live log, colored and copyable. */
export function DeveloperLog() {
  const [bundling, setBundling] = useState(false)
  const bundle = async () => {
    setBundling(true)
    try {
      const r = await daemonClient()?.request<{ path: string }>('diag.bundle', {}, 90_000)
      if (r?.path) {
        toast({ tone: 'ok', title: t('diag.made'), body: r.path }, 8000)
        if (isTauri()) await invoke('reveal_received', { path: r.path }).catch(() => undefined)
      }
    } catch (e) {
      toast({ tone: 'error', title: t('diag.failed'), body: e instanceof Error ? e.message : String(e) }, 9000)
    } finally {
      setBundling(false)
    }
  }
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [level, setLevel] = useState<'all' | 'info' | 'warn'>('info')
  const [paused, setPaused] = useState(false)
  const [copied, setCopied] = useState(false)
  const pausedRef = useRef(paused)
  pausedRef.current = paused
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const c = daemonClient()
    if (!connected || !c) return
    let alive = true
    void c.request<LogEntry[]>('logs.tail', { after: 0 }).then((list) => {
      if (alive) setEntries((list ?? []).slice(-MAX))
    })
    const off = c.on<LogEntry>('daemon.log', (e) => {
      if (pausedRef.current) return
      setEntries((prev) => (prev.length >= MAX ? [...prev.slice(-MAX + 1), e] : [...prev, e]))
    })
    return () => {
      alive = false
      off()
    }
  }, [connected])

  const shown = useMemo(
    () => entries.filter((e) => level === 'all' || (level === 'info' ? e.level !== 'DEBUG' : e.level === 'WARN' || e.level === 'ERROR')),
    [entries, level],
  )

  useEffect(() => {
    const el = box.current
    if (el && !paused) el.scrollTop = el.scrollHeight
  }, [shown, paused])

  const copy = async () => {
    await writeClipboard(shown.map(line).join('\n'))
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  const counts = useMemo(() => ({
    err: entries.filter((e) => e.level === 'ERROR').length,
    warn: entries.filter((e) => e.level === 'WARN').length,
  }), [entries])

  return (
    <Card>
      <SectionTitle
        icon={<Terminal size={16} />}
        extra={
          <div className="flex items-center gap-2">
            <span className="chip border-red-400/40 text-red-300">{counts.err}</span>
            <span className="chip border-amber-400/40 text-amber-200">{counts.warn}</span>
          </div>
        }
      >
        {t('dev.title')}
      </SectionTitle>
      <p className="mb-3 text-xs text-slate-500">{t('dev.hint')}</p>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <select className="input w-auto text-xs" value={level} onChange={(e) => setLevel(e.target.value as typeof level)}>
          <option value="all">{t('dev.levelAll')}</option>
          <option value="info">{t('dev.levelInfo')}</option>
          <option value="warn">{t('dev.levelWarn')}</option>
        </select>
        <button type="button" className="btn btn-sm" onClick={() => setPaused((p) => !p)}>
          {paused ? <Play size={14} /> : <Pause size={14} />} {paused ? t('dev.resume') : t('dev.pause')}
        </button>
        <button type="button" className="btn btn-sm" onClick={() => setEntries([])}>
          <Trash2 size={14} /> {t('dev.clear')}
        </button>
        <button type="button" className="btn btn-sm" onClick={() => void bundle()} disabled={bundling}>
          <FileArchive size={14} /> {bundling ? t('diag.making') : t('diag.make')}
        </button>
        <button type="button" className="btn btn-primary btn-sm" onClick={() => void copy()} disabled={shown.length === 0}>
          <Copy size={14} /> {copied ? t('common.copied') : t('dev.copy')}
        </button>
      </div>
      <div ref={box} className="ltr h-[28rem] overflow-auto rounded-lg border border-white/[0.06] bg-black/40 p-3 font-mono text-[11px] leading-5" dir="ltr">
        {shown.length === 0 ? <div className="text-slate-500">{t('dev.empty')}</div> : null}
        {shown.map((e) => (
          <div key={e.seq} className={`whitespace-pre-wrap break-all ${tone(e)}`}>
            <span className="text-slate-600">{e.time.slice(11, 19)} </span>
            <span className="font-semibold">{e.level.padEnd(5)} </span>
            {e.msg}
            {e.attrs ? <span className="opacity-70"> {e.attrs}</span> : null}
          </div>
        ))}
      </div>
    </Card>
  )
}
