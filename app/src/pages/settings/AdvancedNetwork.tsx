import { SlidersHorizontal } from 'lucide-react'

import { TapDriverNotice } from '../../components/TapDriverNotice'
import { Card, Row, Segmented, SectionTitle, Switch } from '../../components/ui'
import { useT } from '../../i18n'
import type { NetworkSettings, Settings } from '../../types/protocol'

export const DEFAULT_NETWORK: NetworkSettings = {
  mtu: 0,
  interface_priority: true,
  relay_discovery: true,
  broadcast_rate: 0,
  name_resolution: true,
  mesh: true,
  relay_only: false,
  port_min: 0,
  port_max: 0,
  room_mode: 'l3',
}

export function AdvancedNetwork({
  draft,
  setDraft,
  disabled,
}: {
  draft: Settings
  setDraft: (s: Settings) => void
  disabled: boolean
}) {
  const t = useT()
  const n = { ...DEFAULT_NETWORK, ...(draft.network ?? {}) }
  const set = (patch: Partial<NetworkSettings>) => setDraft({ ...draft, network: { ...n, ...patch } })
  const numInput = (value: number, onChange: (v: number) => void, max: number) => (
    <input
      type="number"
      className="input ltr w-24 text-center"
      min={0}
      max={max}
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(Math.max(0, Math.min(max, Number(e.target.value) || 0)))}
    />
  )

  return (
    <Card>
      <SectionTitle icon={<SlidersHorizontal size={16} />}>{t('adv.title')}</SectionTitle>
      <p className="mb-2 text-xs text-slate-500">{t('adv.hint')}</p>
      <div className="divide-y divide-white/[0.05]">
        <Row title={t('adv.mesh')} hint={t('adv.meshHint')}>
          <Switch checked={n.mesh} disabled={disabled} onChange={(v) => set({ mesh: v })} />
        </Row>
        <Row title={t('adv.priority')} hint={t('adv.priorityHint')}>
          <Switch checked={n.interface_priority} disabled={disabled} onChange={(v) => set({ interface_priority: v })} />
        </Row>
        <Row title={t('adv.discovery')}>
          <Switch checked={n.relay_discovery} disabled={disabled} onChange={(v) => set({ relay_discovery: v })} />
        </Row>
        <Row title={t('adv.rate')}>{numInput(n.broadcast_rate, (v) => set({ broadcast_rate: v }), 2000)}</Row>
        <Row title={t('adv.names')} hint={t('adv.namesHint')}>
          <Switch checked={n.name_resolution} disabled={disabled} onChange={(v) => set({ name_resolution: v })} />
        </Row>
        <Row title={t('adv.mtu')} hint={t('adv.mtuHint')}>
          {numInput(n.mtu, (v) => set({ mtu: v }), 1400)}
        </Row>
        <Row title={t('adv.relayOnly')} hint={t('adv.relayOnlyHint')}>
          <Switch checked={n.relay_only} disabled={disabled} onChange={(v) => set({ relay_only: v })} />
        </Row>
        <Row title={t('adv.ports')} hint={t('adv.portsHint')}>
          <div className="flex items-center gap-2">
            {numInput(n.port_min, (v) => set({ port_min: v }), 65535)}
            <span className="text-slate-500">–</span>
            {numInput(n.port_max, (v) => set({ port_max: v }), 65535)}
          </div>
        </Row>
        {n.room_mode === 'l2' ? (
          <div className="py-2">
            <TapDriverNotice />
          </div>
        ) : null}
        <Row title={t('adv.mode')} hint={t('adv.modeHint')}>
          <Segmented<'l3' | 'l2'>
            value={n.room_mode}
            onChange={(v) => !disabled && set({ room_mode: v })}
            options={[
              { value: 'l3', label: t('adv.modeL3') },
              { value: 'l2', label: t('adv.modeL2') },
            ]}
          />
        </Row>
      </div>
    </Card>
  )
}
