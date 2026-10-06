import { invoke } from '@tauri-apps/api/core'
import { Check, Loader2, Wand2, X } from 'lucide-react'
import { useState } from 'react'

import { useT } from '../i18n'
import { isTauri } from '../services/tauri'
import { daemonClient } from '../stores/daemon'
import { toast } from '../stores/toasts'

interface Step {
  step: 'firewall' | 'network' | 'gfwl'
  ok: boolean
  detail?: string
}

interface Result {
  game_id: string
  game_name: string
  steps: Step[]
  needs_path?: boolean
  executables?: string[]
}

/**
 * "Apply" on a game: the firewall, the right network type and game-specific
 * settings, in one click. When LanBaz cannot find the game, it asks for its
 * program with the Windows file picker and applies again.
 */
export function GameApplyButton({ gameId }: { gameId: string }) {
  const t = useT()
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<Result | null>(null)

  const run = async (path?: string) => {
    const c = daemonClient()
    if (!c) return
    setBusy(true)
    try {
      let r = await c.request<Result>('game.apply', { game_id: gameId, path }, 120_000)
      if (r.needs_path && !path && isTauri()) {
        const exe = (r.executables ?? []).join(', ')
        const picked = await invoke<string[]>('pick_files', { title: t('apply.pickExe', { exe }) })
        if (picked[0]) r = await c.request<Result>('game.apply', { game_id: gameId, path: picked[0] }, 120_000)
      }
      setResult(r)
      const allOk = r.steps.every((s) => s.ok)
      toast({ tone: allOk ? 'ok' : 'warn', title: allOk ? t('apply.done', { game: r.game_name }) : t('apply.partly', { game: r.game_name }), key: `apply:${gameId}` })
    } catch (e) {
      toast({ tone: 'error', title: t('apply.failed'), body: e instanceof Error ? e.message : String(e) }, 9000)
    } finally {
      setBusy(false)
    }
  }

  const describe = (s: Step): string => {
    switch (s.step) {
      case 'firewall':
        if (s.detail === 'need_path') return t('apply.fwNeedPath')
        return s.ok ? t('apply.fwOk') : t('apply.fwFail', { why: s.detail ?? '' })
      case 'network':
        if (s.detail === 'switched_l2') return t('apply.netSwitched')
        if (s.detail === 'create_classic') return t('apply.netCreateClassic')
        if (s.detail?.startsWith('already') || s.detail === 'no rooms') return t('apply.netOk')
        return s.ok ? t('apply.netOk') : t('apply.netFail', { why: s.detail ?? '' })
      case 'gfwl':
        if (s.detail === 'set when a room opens') return t('apply.gfwlLater')
        return s.ok ? t('apply.gfwlOk') : t('apply.gfwlFail', { why: s.detail ?? '' })
    }
    return ''
  }

  return (
    <div className="mt-3">
      <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void run()} title={t('apply.hint')}>
        {busy ? <Loader2 size={14} className="animate-spin" /> : <Wand2 size={14} />}
        {busy ? t('apply.applying') : t('apply.apply')}
      </button>
      {result ? (
        <ul className="mt-2 space-y-1">
          {result.steps.map((s) => (
            <li key={s.step} className={`flex items-start gap-1.5 text-[11px] ${s.ok ? 'text-emerald-300/90' : 'text-amber-200/90'}`}>
              {s.ok ? <Check size={12} className="mt-0.5 shrink-0" /> : <X size={12} className="mt-0.5 shrink-0" />}
              <span>{describe(s)}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}
