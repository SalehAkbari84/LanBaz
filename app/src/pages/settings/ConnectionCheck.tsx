import { Activity, CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { useState } from 'react'

import { Banner, Card, Row, SectionTitle } from '../../components/ui'
import { useT } from '../../i18n'
import { useDaemonStore } from '../../stores/daemon'
import type { DiagnoseReport, DiagnoseServer } from '../../types/protocol'

/** Settings → Network: tells a user whether this PC can make direct links. */
export function ConnectionCheck() {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const diagnose = useDaemonStore((s) => s.diagnose)
  const [running, setRunning] = useState(false)
  const [report, setReport] = useState<DiagnoseReport | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    setRunning(true)
    setError(null)
    try {
      setReport(await diagnose())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  const tone = !report ? 'info' : report.advice === 'ok' ? 'ok' : report.advice === 'needs_turn' ? 'warn' : 'error'

  return (
    <Card>
      <SectionTitle
        icon={<Activity size={16} />}
        extra={
          <button type="button" className="btn btn-primary btn-sm" disabled={!connected || running} onClick={() => void run()}>
            {running ? <Loader2 size={14} className="animate-spin" /> : <Activity size={14} />}
            {running ? t('diag.running') : t('diag.run')}
          </button>
        }
      >
        {t('diag.title')}
      </SectionTitle>
      <p className="mb-3 text-xs text-slate-500">{t('diag.hint')}</p>
      {error ? <Banner tone="error">{error}</Banner> : null}
      {report ? (
        <div className="flex flex-col gap-3">
          <Banner tone={tone}>{t(`diag.advice.${report.advice}`)}</Banner>
          <Row title={t('diag.nat')}>
            <span className="text-sm text-slate-200">{t(`diag.nat.${report.nat}`)}</span>
          </Row>
          {report.port_mapping ? (
            <Row title={t('diag.portMapping')} hint={/^(UPnP|NAT-PMP) /.test(report.port_mapping) ? t('diag.portMappingOk') : t('diag.portMappingOff')}>
              <span className={`ltr text-xs ${/^(UPnP|NAT-PMP) /.test(report.port_mapping) ? 'text-emerald-300' : 'text-slate-400'}`}>
                {/^(UPnP|NAT-PMP) /.test(report.port_mapping) ? report.port_mapping : t('diag.portMappingNone')}
              </span>
            </Row>
          ) : null}
          {report.public_ip ? (
            <Row title={t('diag.publicIp')}>
              <span className="ltr font-mono text-sm text-slate-200">{report.public_ip}</span>
            </Row>
          ) : null}
          <Row title={t('diag.servers')}>
            <span className="ltr text-sm text-slate-200">
              {report.working.length} / {report.stun.length}
            </span>
          </Row>
          <details className="rounded-lg border border-white/[0.06] bg-white/[0.02] p-3 text-xs">
            <summary className="cursor-pointer text-slate-400">{t('diag.details')}</summary>
            <ServerList items={report.stun} />
            {report.turn.length > 0 ? (
              <>
                <div className="mt-3 text-slate-400">{t('diag.relays')}</div>
                <ServerList items={report.turn} />
              </>
            ) : null}
          </details>
        </div>
      ) : null}
    </Card>
  )
}

function ServerList({ items }: { items: DiagnoseServer[] }) {
  return (
    <ul className="ltr mt-2 flex flex-col gap-1 font-mono">
      {items.map((s) => (
        <li key={s.server} className="flex items-center gap-2">
          {s.ok ? <CheckCircle2 size={12} className="shrink-0 text-emerald-400" /> : <XCircle size={12} className="shrink-0 text-red-400" />}
          <span className="truncate text-slate-300">{s.server.replace(/^(stun|turn):/, '')}</span>
          <span className="ms-auto shrink-0 text-slate-500">{s.ok ? `${s.rtt_ms ?? 0} ms · ${s.mapped ?? ''}` : s.error}</span>
        </li>
      ))}
    </ul>
  )
}
