import { api } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'

/**
 * The tool egress in one line, for the rail footer: the same instance-wide
 * selection the settings page owns, shown wherever the operator happens to be
 * (a project workspace included). The full panel with the probe button stays in
 * settings; this only repeats the last measurement, so it never starts work of
 * its own beyond the poll the panel already caches.
 *
 * The egress belongs to the scan node, not to a project: every project on that
 * node dials the same upstream, so this chip is deliberately not per-project.
 */
export function EgressChip() {
  const status = usePoll(() => api.egress('cached'), 30_000, [])
  const data = status.data
  const probe = data?.probe
  const failed = !!(data?.invalid || data?.probe_error || (probe && !probe.ok))
  const dot = failed ? 'bg-destructive' : probe?.ok ? 'bg-success' : 'bg-ink-3'
  const label = !data ? '出口 读取中'
    : data.mode === 'direct' ? '出口 直连'
    : data.mode === 'single' ? '出口 单节点'
    : data.mode === 'subscription' ? '出口 订阅'
    : '出口 未知'
  const detail = !data ? ''
    : data.probe_error ? '检测失败'
    : !probe ? '未检测'
    : probe.ok ? `${probe.duration_ms}ms`
    : '不通'
  const title = !data ? '正在读取出口状态'
    : data.probe_error ? `检测无法运行：${data.probe_error}`
    : probe ? `${label}${detail ? ` · ${detail}` : ''} · ${probe.target}` : `${label} · 尚未检测`
  return <button
    type="button"
    onClick={() => navigate('/g/?view=settings')}
    title={title}
    aria-label={`${title}（打开设置）`}
    className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-caption text-ink-2 transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
  >
    <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${dot}`} />
    <span className="min-w-0 flex-1 truncate">{label}{detail ? ` · ${detail}` : ''}</span>
  </button>
}
