import type {
  MCPServerConfig,
  ProjectInfo,
  SettingsCheck,
  SettingsGuardrailSummary,
  SettingsView,
} from './types.ts'

export function nonNegativeInteger(raw: string, label: string): number {
  const value = raw.trim()
  if (!value) return 0
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error(`${label}必须是非负整数`)
  return Number(value)
}

export function durationLabel(seconds?: number): string {
  if (seconds == null) return '…'
  if (seconds <= 0) return '不限'
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

export type ConnectionNumberField = 'context_window' | 'max_tokens'

export interface ConnectionFieldProblem {
  field: ConnectionNumberField
  message: string
}

/**
 * The two optional connection numbers, checked before the request so the error
 * can name the field instead of only the payload. An empty value means "leave
 * it to the gateway" and is valid; only a typed value must be a non-negative
 * integer. Kept pure so the rule is testable without a browser.
 */
export function connectionProblems(draft: Record<ConnectionNumberField, string>): ConnectionFieldProblem[] {
  const problems: ConnectionFieldProblem[] = []
  for (const field of ['context_window', 'max_tokens'] as const) {
    const raw = draft[field].trim()
    if (!raw) continue
    if (!/^\d+$/.test(raw) || !Number.isSafeInteger(Number(raw))) {
      problems.push({
        field,
        message: field === 'context_window' ? '上下文窗口必须是非负整数（留空交给网关）' : '输出上限必须是非负整数（留空交给网关）',
      })
    }
  }
  return problems
}

/** Runtime concurrency is an integer in [1, max]; empty/garbage is rejected. */
export function concurrencyProblem(raw: string, max: number): string | null {
  const value = raw.trim()
  if (!/^\d+$/.test(value)) return `并发必须是 1–${max} 的整数`
  if (!Number.isSafeInteger(Number(value)) || Number(value) < 1 || Number(value) > max) return `并发必须是 1–${max} 的整数`
  return null
}

export type ProjectSettingsDraft = {
  max_runs: string
  round_timeout: string
}

export function projectSettingsDraft(p: ProjectInfo): ProjectSettingsDraft {
  return {
    max_runs: p.max_runs == null ? '' : String(p.max_runs), round_timeout: p.round_timeout ?? '',
  }
}

/** Refresh clean fields while preserving each field the operator has edited. */
export function mergeProjectSettings(previous: ProjectSettingsDraft, incoming: ProjectSettingsDraft, draft: ProjectSettingsDraft): ProjectSettingsDraft {
  const next = { ...draft }
  for (const key of Object.keys(incoming) as (keyof ProjectSettingsDraft)[]) {
    if (draft[key] === previous[key]) next[key] = incoming[key]
  }
  return next
}

/** Only changed fields are sent: saving concurrency must not overwrite routing.
 *  The brief and the constraint document are not settings any more — they are the
 *  `scope`/`constraints` documents, edited through PUT /api/documents/{kind} with
 *  a revision. The project endpoint rejects those two fields, so a stale caller
 *  could only fail; they are gone from this draft on purpose. Neither retired
 *  field rides along either: verify_auto (no review switch) and premise_policy
 *  (one fixed mode) are rejected by the server, so sending them would fail. */
export function projectSettingsPatch(previous: ProjectSettingsDraft, draft: ProjectSettingsDraft) {
  const patch: { max_runs?: number | null; round_timeout?: string } = {}
  if (draft.max_runs !== previous.max_runs) patch.max_runs = draft.max_runs.trim() ? nonNegativeInteger(draft.max_runs, '项目并发') : null
  if (draft.round_timeout !== previous.round_timeout) patch.round_timeout = draft.round_timeout.trim()
  return patch
}

// ---- 扩展工具：MCP 服务器与 Tavily key ----
//
// The product sections (extensions.mcp / extensions.search) are edited as one
// JSON object because the harness's own shape is nested; a structured row
// editor would still have to serialise commands, env, headers and timeouts. The
// validation here mirrors internal/settings/product.go so the operator sees the
// alias/field problem before the request round-trip; the server stays the
// authority. Values read back as "***" (masked secret) are kept as-is: the
// server merges them with the stored value instead of writing the placeholder.

const mcpAliasPattern = /^[a-zA-Z0-9_-]+$/
const mcpKnownFields = new Set([
  'command', 'args', 'env', 'cwd', 'url', 'headers', 'tools', 'startupTimeoutSeconds', 'timeoutSeconds',
])

export interface MCPServersParse {
  /** The object to PUT, or null when the draft is invalid. */
  value: Record<string, MCPServerConfig> | null
  error?: string
  /** Non-blocking notes (unknown fields that will be dropped). */
  warnings: string[]
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function stringRecord(value: unknown, label: string): Record<string, string> | string {
  if (!isRecord(value)) return `${label}必须是 JSON 对象`
  const out: Record<string, string> = {}
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry !== 'string') return `${label}的值必须是字符串（${key}）`
    out[key] = entry
  }
  return out
}

/** The masked-read JSON shown in the editor; empty when nothing is stored. */
export function mcpServersDraftText(servers?: Record<string, MCPServerConfig> | null): string {
  if (!servers || Object.keys(servers).length === 0) return ''
  try {
    return JSON.stringify(servers, null, 2)
  } catch {
    return ''
  }
}

/**
 * Parse and validate the MCP editor's draft. Empty text or `{}` is the explicit
 * "remove every server" value (the API distinguishes nil = keep from {} = clear);
 * any typed server is checked against the same rules the host applies.
 */
export function parseMCPServersDraft(text: string): MCPServersParse {
  const trimmed = text.trim()
  if (!trimmed || trimmed === '{}') return { value: {}, warnings: [] }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch (e) {
    return { value: null, error: `不是合法 JSON：${e instanceof Error ? e.message : String(e)}`, warnings: [] }
  }
  if (!isRecord(parsed)) return { value: null, error: 'MCP 服务器必须是 JSON 对象（别名 → 配置）', warnings: [] }
  const warnings: string[] = []
  const out: Record<string, MCPServerConfig> = {}
  for (const [alias, raw] of Object.entries(parsed)) {
    if (!mcpAliasPattern.test(alias)) {
      return { value: null, error: `服务器别名「${alias}」只能用字母、数字、- 和 _`, warnings }
    }
    if (!isRecord(raw)) return { value: null, error: `服务器 ${alias} 必须是 JSON 对象`, warnings }
    const unknown = Object.keys(raw).filter((key) => !mcpKnownFields.has(key))
    if (unknown.length > 0) warnings.push(`服务器 ${alias} 的未知字段会被忽略：${unknown.join('、')}`)
    const server: MCPServerConfig = {}
    const command = typeof raw.command === 'string' ? raw.command.trim() : ''
    const url = typeof raw.url === 'string' ? raw.url.trim() : ''
    if (!command === !url) return { value: null, error: `服务器 ${alias} 必须且只能配置 command 或 url 之一`, warnings }
    if (typeof raw.args === 'string') return { value: null, error: `服务器 ${alias} 的 args 必须是字符串数组`, warnings }
    if (raw.args !== undefined) {
      if (!Array.isArray(raw.args) || raw.args.some((arg) => typeof arg !== 'string')) {
        return { value: null, error: `服务器 ${alias} 的 args 必须是字符串数组`, warnings }
      }
      server.args = [...(raw.args as string[])]
    }
    if (typeof raw.cwd === 'string' && raw.cwd.trim()) server.cwd = raw.cwd.trim()
    if (raw.env !== undefined) {
      const env = stringRecord(raw.env, `服务器 ${alias} 的 env`)
      if (typeof env === 'string') return { value: null, error: env, warnings }
      server.env = env
    }
    if (raw.headers !== undefined) {
      const headers = stringRecord(raw.headers, `服务器 ${alias} 的 headers`)
      if (typeof headers === 'string') return { value: null, error: headers, warnings }
      server.headers = headers
    }
    if (raw.tools !== undefined && raw.tools !== null) {
      if (!Array.isArray(raw.tools) || raw.tools.some((tool) => typeof tool !== 'string' || !tool.trim())) {
        return { value: null, error: `服务器 ${alias} 的 tools 必须是非空字符串数组`, warnings }
      }
      const tools = (raw.tools as string[]).map((tool) => tool.trim())
      if (new Set(tools).size !== tools.length) return { value: null, error: `服务器 ${alias} 的 tools 有重复项`, warnings }
      server.tools = tools
    } else {
      // Absent (or the read's explicit null) mounts every tool; that is
      // distinct from an explicit [] which mounts none.
      server.tools = null
    }
    for (const key of ['startupTimeoutSeconds', 'timeoutSeconds'] as const) {
      const value = raw[key]
      if (value === undefined) continue
      if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
        return { value: null, error: `服务器 ${alias} 的 ${key} 必须是非负整数`, warnings }
      }
      server[key] = value
    }
    if (command) {
      server.command = command
      if (server.headers && Object.keys(server.headers).length > 0) {
        return { value: null, error: `服务器 ${alias}：headers 只能与 url 一起使用`, warnings }
      }
    } else {
      if ((server.args?.length ?? 0) > 0 || (server.env && Object.keys(server.env).length > 0) || server.cwd) {
        return { value: null, error: `服务器 ${alias}：args/env/cwd 只能与 command 一起使用`, warnings }
      }
      let parsedURL: URL
      try {
        parsedURL = new URL(url)
      } catch {
        return { value: null, error: `服务器 ${alias} 的 url 不是合法 URL`, warnings }
      }
      if (!['http:', 'https:'].includes(parsedURL.protocol) || !parsedURL.hostname) {
        return { value: null, error: `服务器 ${alias} 的 url 必须是 HTTP(S) 绝对地址`, warnings }
      }
      if (parsedURL.username || parsedURL.password || parsedURL.hash) {
        return { value: null, error: `服务器 ${alias} 的 url 不能带用户名/密码/片段；认证信息放在 headers`, warnings }
      }
      server.url = url
    }
    out[alias] = server
  }
  return { value: out, warnings }
}

/** The model half of a PUT /api/settings body. A product-only save must carry
 *  it: the server applies the whole request view, so omitting the model fields
 *  would clear the gateway connection. */
export function settingsModelPayload(
  view: Pick<SettingsView, 'base_url' | 'model' | 'provider' | 'context_window' | 'max_tokens'>,
): Pick<SettingsView, 'base_url' | 'model' | 'provider' | 'context_window' | 'max_tokens'> {
  return {
    base_url: view.base_url,
    model: view.model,
    provider: view.provider || '',
    context_window: view.context_window || 0,
    max_tokens: view.max_tokens || 0,
  }
}

/**
 * The save outcome notice. "online" means the running host accepted the change
 * without a restart; "restart" means it is restarting for it; "saved" without a
 * live host means it applies at the next launch. A product change always needs
 * the restart (MCP/search are read at scan start), so requires_restart wins.
 */
export function settingsApplyNotice(
  view: Pick<SettingsView, 'applied' | 'requires_restart' | 'restart_reason'>,
): { tone: 'success' | 'warning' | 'info'; text: string } | null {
  const reason = view.restart_reason?.trim()
  switch (view.applied) {
    case 'online':
      return { tone: 'success', text: '已在线应用：运行中的宿主已接受新配置，之后的会话直接使用新值，无需重启。' }
    case 'restart':
      return { tone: 'warning', text: `配置已保存，本机宿主正在按新配置重启，就绪后生效。${reason ? `原因：${reason}` : ''}` }
    case 'saved':
      if (view.requires_restart) {
        return { tone: 'warning', text: `已保存；需要重启后生效。${reason ? `原因：${reason}` : ''}` }
      }
      return { tone: 'info', text: `已保存。${reason ? `说明：${reason}` : '本机没有运行中的宿主时，下次启动生效。'}` }
    default:
      if (view.requires_restart) {
        return { tone: 'warning', text: `已保存；需要重启后生效。${reason ? `原因：${reason}` : ''}` }
      }
      return null
  }
}

/** The instance model catalogue read (GET /api/models) — limits are per model. */
export function modelCatalogSummary(
  catalog: { models: string[]; limits?: Record<string, number>; source?: string; base_url?: string },
): string {
  const models = catalog.models ?? []
  if (models.length === 0) return '网关没有返回可用模型'
  const source = catalog.source === 'host' ? '运行中的宿主' : '控制器探测'
  return `${source}返回 ${models.length} 个模型${catalog.base_url ? `（${catalog.base_url}）` : ''}`
}

/** The connectivity check answer as one operator sentence. */
export function checkProbeSummary(result: SettingsCheck): { tone: 'success' | 'warning'; text: string } {
  const source = result.source === 'host' ? '运行中的宿主实测' : result.source === 'self' ? '控制器自检（宿主未在线）' : '来源未标注'
  if (!result.available) {
    return { tone: 'warning', text: `连接不可用（${source}）：${result.error?.trim() || '宿主没有返回原因'}` }
  }
  const parts = [`连接可用（${source}）`]
  if (result.returned_model?.trim()) parts.push(`返回模型 ${result.returned_model.trim()}`)
  if (typeof result.ms === 'number' && result.ms >= 0) parts.push(`耗时 ${result.ms} ms`)
  if (result.models?.length) parts.push(`模型目录 ${result.models.length} 个`)
  return { tone: 'success', text: `${parts.join(' · ')}。` }
}

/** Tavily key presence: only the count is returned, never the keys. */
export function searchKeyStatus(view: Pick<SettingsView, 'search_has_key' | 'search_key_count'>): string {
  // 空 key 不是"搜索不可用"：harness 会回落到 DuckDuckGo（exts/search/config.go
  // 的默认值、tools/search/tavily.go 的 searchDDG）。文案要说出回落，否则
  // 操作员会以为没有搜索能力而放弃配置或误判能力边界。
  if (!view.search_has_key) return '未配置 Tavily key，搜索回落到 DuckDuckGo（只给标题与摘要，易被限流）'
  const count = view.search_key_count ?? 0
  return count > 0 ? `已保存 ${count} 个 Tavily key（值不回显）` : '已保存 Tavily key（值不回显）'
}

// ---- 守门策略（extensions.guardrail，只读投影） ----
//
// The judge itself is configured by the operator in cyber.yaml; the settings
// page only reports what is in effect. Two facts must never be dressed up:
// provider none is "not enabled" (neutral, not an error), and on_error allow is
// an explicit fail-open tradeoff the console is required to spell out — a
// let-through is not a passed judgement (docs/01-architecture.md §10.3).

export const guardrailFailOpenText =
  '守门未生效时自动放行（fail-open）：裁判不可用、超时或输出不合法时会直接放行被拦截的工具调用。这是显式的安全取舍，放行不等于已判定通过。'

export interface GuardrailPolicyFlags {
  enabled: boolean
  failOpen: boolean
  shadow: boolean
  label: string
}

/** The three facts the card shows as badges, kept pure for the tests. */
export function guardrailPolicyFlags(guardrail?: SettingsGuardrailSummary): GuardrailPolicyFlags {
  const provider = guardrail?.provider?.trim() ?? ''
  const enabled = Boolean(guardrail?.configured) && provider !== '' && provider !== 'none'
  return {
    enabled,
    failOpen: guardrail?.on_error === 'allow',
    shadow: Boolean(guardrail?.shadow),
    label: enabled ? '守门已启用' : '守门未启用',
  }
}

export function guardrailProviderLabel(provider?: string): string {
  switch (provider?.trim()) {
    case 'none':
      return 'none（不做自动判定）'
    case 'jev':
      return 'jev（JEV 两阶段：风险 + 后果）'
    case 'llm':
      return 'llm（OpenAI 兼容小模型裁判）'
    case 'hybrid':
      return 'hybrid（先小模型判定，REVIEW 再交 JEV）'
    case '':
    case undefined:
      return '未设置（等同 none）'
    default:
      return provider!.trim()
  }
}

/** `scope` narrows by tool name; an empty list means every tool. */
export function guardrailScopeLabel(scope?: string[]): string {
  const tools = (scope ?? []).map((tool) => tool.trim()).filter(Boolean)
  return tools.length === 0 ? '全部工具' : tools.join('、')
}

/** Number of tool names the policy actually judges (0 = every tool). */
export function guardrailScopeCount(scope?: string[]): number {
  return (scope ?? []).map((tool) => tool.trim()).filter(Boolean).length
}

/** `bash、subagent（2 个工具）` or `全部工具（未设置 scope）`. */
export function guardrailScopeSummary(scope?: string[]): string {
  const count = guardrailScopeCount(scope)
  return count === 0 ? '全部工具（未设置 scope）' : `${guardrailScopeLabel(scope)}（${count} 个工具）`
}

/** Prefix allow-list row: the count plus the safe-mode-only caveat. */
export function guardrailAllowPrefixesSummary(count?: number): string {
  const total = typeof count === 'number' && count > 0 ? count : 0
  return total > 0 ? `${total} 条（仅 mode=safe 生效）` : '未配置（仅 mode=safe 生效）'
}

/** True when the configured judge actually calls the small model (LLM/hybrid). */
export function guardrailUsesLLM(provider?: string): boolean {
  const value = provider?.trim()
  return value === 'llm' || value === 'hybrid'
}

/**
 * How to enable the judge — the operator-visible mental model. `none` is the
 * default; credentials alone never enable a policy; JEV is optional (and may be
 * unavailable from mainland China, which is why llm-only is a supported mode).
 */
export const guardrailEnableText =
  '启用方式：不配置或 provider=none 就是不启用（只配置凭据不会启用策略）。可以只配 provider: llm 和它的 llm 子段，不配 JEV；JEV 只是可选的复核裁判（hybrid 使用），国内环境无法访问 JEV 出口时可以完全不用。'

/**
 * The scope bypass surface — docs/harness configuration.md and user/tools.md say
 * the same: scope is a reduction of the checked surface, not an isolation
 * boundary.
 */
export const guardrailScopeExplainText =
  'scope 只按工具名过滤：未列入的工具不会经过检查。这是缩小检查面（降低判定成本与审批噪音），不是安全隔离——bash 内部命令、子会话、MCP/命令适配层或其他入口仍可能执行同样的操作，scope 不能替代宿主权限、沙箱或网络边界。'

/**
 * `allow_prefixes` is literal prefix matching, safe mode only. The `ls`/`lsof`
 * example is the canonical warning from the harness docs.
 */
export const guardrailAllowPrefixesExplainText =
  'allow_prefixes 仅在 mode=safe 下生效：命令文本按字面前缀命中时直接放行、不进入人工审批。它不做语义分析（例如 ls 会命中 lsof；命令拼接 ls; rm -rf / 也以 ls 开头），只应放入确定低风险、无副作用的前缀。'

/**
 * The LLM judge's decision policy: a strong ALLOW bias, not a risk scanner.
 * Failure/timeout follows `on_error` (default review; allow is fail-open).
 */
export const guardrailLLMSemanticsText =
  'LLM 裁判默认允许：不确定或没把握时允许；只有明确破坏可用性、数据完整性或系统配置才拦截。裁判失败/超时按 on_error 处理：默认转人工复核，也可显式 allow（fail-open）或 block。'

/** The judge's LLM sub-section as one line, never including a key. */
export function guardrailLLMLabel(llm?: SettingsGuardrailSummary['llm']): string {
  if (!llm?.configured) return '未配置'
  const parts = ['已配置']
  if (llm.model?.trim()) parts.push(llm.model.trim())
  if (llm.base_url?.trim()) parts.push(llm.base_url.trim())
  return parts.join(' · ')
}

// ---- 流量捕获保留上限（scan 节点范围，所有项目共享） ----
//
// One number: how much body bytes the scan node keeps in its capture directory
// before rotating old bodies out. It is NOT a per-project quota and saker keeps
// no project body copy of its own. The console edits MiB; the wire field is
// bytes named traffic_body_retention_bytes; 0/absent means the generated default
// (2 GiB) applies and is displayed as "默认 2 GiB（未显式设置）", never as 0.

export const TRAFFIC_RETENTION_DEFAULT_MIB = 2048
export const TRAFFIC_RETENTION_DEFAULT_BYTES = TRAFFIC_RETENTION_DEFAULT_MIB * 1024 * 1024
export const TRAFFIC_RETENTION_MIN_MIB = 16
export const TRAFFIC_RETENTION_MAX_MIB = 1_048_576
export const MIB_BYTES = 1024 * 1024

/** Bytes for an operator-typed MiB value (0 = no explicit value / default). */
export function retentionBytesFromMib(mib: number): number {
  if (!Number.isSafeInteger(mib) || mib <= 0) return 0
  return mib * MIB_BYTES
}

/** The MiB shown in the input; 0 when the server reports 0/absent/invalid. */
export function retentionMibFromBytes(bytes?: number): number {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) return 0
  return Math.round(bytes / MIB_BYTES)
}

/** The input value: empty (not "0") when no explicit value is stored. */
export function retentionDraft(bytes?: number): string {
  const mib = retentionMibFromBytes(bytes)
  return mib > 0 ? String(mib) : ''
}

/**
 * Local validation for the MiB field. Empty is valid and means "remove the
 * explicit value, use the generated default"; anything else must be an integer
 * in [16 MiB, 1 TiB].
 */
export function retentionProblem(raw: string): string | null {
  const value = raw.trim()
  if (!value) return null
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))) return '保留上限必须是 MiB 整数'
  const mib = Number(value)
  if (mib < TRAFFIC_RETENTION_MIN_MIB) return `最小 ${TRAFFIC_RETENTION_MIN_MIB} MiB`
  if (mib > TRAFFIC_RETENTION_MAX_MIB) return '最大 1,048,576 MiB（1 TiB）'
  return null
}

/** The bytes to PUT for a validated draft ('' = 0 = default). */
export function retentionBytesFromDraft(raw: string): number {
  const value = raw.trim()
  return value ? retentionBytesFromMib(Number(value)) : 0
}

/** `2 GiB` / `512 MiB` / `1 TiB` for the status line. */
export function retentionHuman(bytes: number): string {
  if (bytes >= 1024 * 1024 * 1024) {
    const gib = bytes / (1024 * 1024 * 1024)
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)} GiB`
  }
  return `${Math.round(bytes / MIB_BYTES)} MiB`
}

/**
 * The stored value as operator language. 0/absent is the generated default, not
 * a measured zero — the card must never print "0 MiB".
 */
export function retentionStatusText(bytes?: number): string {
  const mib = retentionMibFromBytes(bytes)
  if (mib <= 0) return '默认 2 GiB（未显式设置）'
  return `已设置 ${mib.toLocaleString('en-US')} MiB（${retentionHuman(mib * MIB_BYTES)}）`
}
