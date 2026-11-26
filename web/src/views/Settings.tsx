import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { Cable, Check, Eye, EyeOff, FolderCog, KeyRound, TriangleAlert } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Input, Skeleton, Spinner, Switch, Textarea } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { GuardrailPolicyCard } from '@/components/GuardrailPolicyCard'
import { ProjectSettingsCard } from '@/components/ProjectSettingsCard'
import { ApiError, api } from '@/lib/api'
import { useLoad, type AsyncState } from '@/lib/hooks'
import {
  checkProbeSummary,
  concurrencyProblem,
  connectionProblems,
  mcpServersDraftText,
  parseMCPServersDraft,
  retentionBytesFromDraft,
  retentionDraft,
  retentionProblem,
  retentionStatusText,
  searchKeyStatus,
  settingsApplyNotice,
  settingsModelPayload,
  TRAFFIC_RETENTION_MAX_MIB,
  TRAFFIC_RETENTION_MIN_MIB,
  type ConnectionNumberField,
} from '@/lib/settings'
import type { RuntimeSettings, SettingsCheck, SettingsView } from '@/lib/types'

const SETTINGS_TABS = [
  { id: 'model', label: '模型连接', icon: Cable },
  { id: 'projects', label: '项目设置', icon: FolderCog },
] as const

type SettingsTab = (typeof SETTINGS_TABS)[number]['id']

export function SettingsPage() {
 const settings = useLoad(api.settings, [])
 const index = useLoad(api.projectIndex, [])
 const runtime = useLoad(api.runtimeSettings, [])
 const [tab, setTab] = useState<SettingsTab>('model')
 const [dirtyProjects, setDirtyProjects] = useState<Set<string>>(new Set())
 const onDirty = useCallback((id: string, dirty: boolean) => {
  setDirtyProjects(current => {
   if (current.has(id) === dirty) return current
   const next = new Set(current)
   if (dirty) next.add(id)
   else next.delete(id)
   return next
  })
 }, [])
 const hasProjectDrafts = dirtyProjects.size > 0
 useEffect(() => {
  if (!hasProjectDrafts) return
  const prevent = (event: BeforeUnloadEvent) => {
   event.preventDefault()
   event.returnValue = ''
  }
  window.addEventListener('beforeunload', prevent)
  return () => window.removeEventListener('beforeunload', prevent)
 }, [hasProjectDrafts])
 return <Page title="设置" subtitle="管理模型网关、扩展工具和各项目的运行参数。">
  <div className="saker-settings @container mx-auto w-full max-w-[104rem] space-y-6">
   <SettingsTabs tab={tab} onTab={setTab} />
   <div role="tabpanel" id="settings-panel-model" aria-labelledby="settings-tab-model" hidden={tab !== 'model'} className="space-y-7">
    <section aria-label="模型与扩展" className="space-y-3">
     <h2 className="text-h4 font-semibold text-ink">模型与扩展</h2>
     <div className="grid items-start gap-4 @min-[70rem]:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <ModelConnection settings={settings} />
      <ExtensionsPanel settings={settings} />
     </div>
    </section>
    <section aria-label="运行默认值" className="space-y-3">
     <h2 className="text-h4 font-semibold text-ink">运行默认值</h2>
     <div className="grid items-start gap-4 @min-[58rem]:grid-cols-2">
      <ReviewDefaultPanel runtime={runtime} />
      <RuntimeConcurrencyPanel runtime={runtime} />
      <TrafficRetentionPanel settings={settings} />
      {settings.data?.guardrail && <GuardrailPolicyCard guardrail={settings.data.guardrail} />}
     </div>
    </section>
   </div>
   <div role="tabpanel" id="settings-panel-projects" aria-labelledby="settings-tab-projects" hidden={tab !== 'projects'} className="space-y-4">
    {index.error && <Callout tone="destructive">{index.error}</Callout>}
    {index.loading && !index.data && <Skeleton className="h-36 w-full rounded-xl" />}
    {index.data && index.data.projects.length === 0 && <EmptyState title="暂无项目" description="创建项目后，可在这里调整它的运行参数。" />}
    {index.data && (
     <div className="grid items-start gap-4 @min-[70rem]:grid-cols-2">
      {index.data.projects.map(project => <ProjectSettingsCard key={project.id} project={project} defaults={index.data!} onSaved={index.reload} onDirty={onDirty} />)}
     </div>
    )}
   </div>
  </div>
 </Page>
}

/**
 * Two tabs, one keyboard stop: the standard tablist roving-tabindex pattern.
 * The panels stay in the DOM (`hidden` removes them from the tab order and the
 * accessibility tree), so switching never refetches and a half-written form is
 * not unmounted. Arrow keys move and activate, Home/End jump to the ends.
 */
function SettingsTabs({ tab, onTab }: { tab: SettingsTab; onTab: (tab: SettingsTab) => void }) {
 const buttons = useRef<(HTMLButtonElement | null)[]>([])
 function select(index: number) {
  const next = ((index % SETTINGS_TABS.length) + SETTINGS_TABS.length) % SETTINGS_TABS.length
  onTab(SETTINGS_TABS[next].id)
  buttons.current[next]?.focus()
 }
 function onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
  const current = SETTINGS_TABS.findIndex((item) => item.id === tab)
  switch (event.key) {
   case 'ArrowRight': event.preventDefault(); select(current + 1); break
   case 'ArrowLeft': event.preventDefault(); select(current - 1); break
   case 'Home': event.preventDefault(); select(0); break
   case 'End': event.preventDefault(); select(SETTINGS_TABS.length - 1); break
   default: break
  }
 }
 return <div role="tablist" aria-label="设置分栏" onKeyDown={onKeyDown} className="saker-settings-tabs inline-flex max-w-full gap-1 rounded-xl border border-hairline bg-surface-2/60 p-1">
  {SETTINGS_TABS.map(({ icon: Icon, ...item }, index) => (
   <Button
    key={item.id}
    ref={(node) => { buttons.current[index] = node }}
    role="tab"
    id={`settings-tab-${item.id}`}
    aria-selected={tab === item.id}
    aria-controls={`settings-panel-${item.id}`}
    tabIndex={tab === item.id ? 0 : -1}
    variant="ghost"
    className="min-w-0 gap-2 px-3.5"
    onClick={() => onTab(item.id)}
   ><Icon className="size-4 shrink-0" aria-hidden="true" />{item.label}</Button>
  ))}
 </div>
}

/**
 * The cyber-ui <Field> is a label wrapper with an implicit association; these
 * settings fields need explicit for/id plus hint and error ids so the control's
 * `aria-describedby` can point at the hint and only at the error while it is
 * shown. Same type ramp and spacing as Field, so the panel keeps its look.
 */
function SettingsField({ id, label, hint, hintID, error, errorID, children }: {
 id: string
 label: ReactNode
 hint?: ReactNode
 hintID?: string
 error?: ReactNode
 errorID?: string
 children: ReactNode
}) {
 return <div className="block space-y-1.5">
  <label htmlFor={id} className="block text-ui font-medium text-ink-2">{label}</label>
  {children}
  {error ? (
   <span id={errorID} className="flex items-start gap-1 text-[11px] leading-4 text-destructive">
    <TriangleAlert aria-hidden="true" className="mt-0.5 h-3 w-3 shrink-0" />
    <span>{error}</span>
   </span>
  ) : hint ? <span id={hintID} className="block text-[11px] leading-4 text-ink-3">{hint}</span> : null}
 </div>
}

type ConnectionDraft = {base_url:string; api_key:string; model:string; provider:string; context_window:string; max_tokens:string}
function connectionDraft(value: SettingsView): ConnectionDraft {
 return {base_url:value.base_url||'', api_key:'', model:value.model||'', provider:value.provider||'openai', context_window:value.context_window ? String(value.context_window) : '', max_tokens:value.max_tokens ? String(value.max_tokens) : ''}
}
function ModelConnection({settings}:{settings:AsyncState<SettingsView>}) {
 const baseline = useRef<ConnectionDraft>()
 const [draft,setDraft] = useState<ConnectionDraft>({base_url:'',api_key:'',model:'',provider:'openai',context_window:'',max_tokens:''})
 const [busy,setBusy] = useState<'save' | 'check' | null>(null)
 const lock=useRef(false)
 const [error,setError] = useState('')
 // The last explicit connectivity probe: rendered with its source so "host
 // measured" and "controller self-probe" are not confused.
 const [probe,setProbe] = useState<SettingsCheck>()
 // The save outcome ("online" | "restart" | "saved"), which is not the same as
 // "persisted": MCP/search changes need the scan restart to take effect.
 const [apply,setApply] = useState<{tone:'success'|'warning'|'info';text:string}|null>(null)
 const [fieldErrors,setFieldErrors] = useState<Partial<Record<ConnectionNumberField,string>>>({})
 const [revealKey,setRevealKey] = useState(false)
 const uid = useId()
 const id = (name: string) => `${uid}-${name}`
 const contextRef = useRef<HTMLInputElement>(null)
 const maxTokensRef = useRef<HTMLInputElement>(null)
 useEffect(()=>{
  if (!settings.data) return
  const next=connectionDraft(settings.data), old=baseline.current
  baseline.current=next
  setDraft(current=>{const merged={...current}; for(const key of Object.keys(next) as (keyof ConnectionDraft)[]) if(!old || current[key]===old[key]) merged[key]=next[key]; return merged})
 },[settings.data])
 const dirty=baseline.current && Object.keys(draft).some(key=>draft[key as keyof ConnectionDraft]!==baseline.current![key as keyof ConnectionDraft])
 useEffect(()=>{if(!dirty)return; const prevent=(e:BeforeUnloadEvent)=>{e.preventDefault();e.returnValue=''}; window.addEventListener('beforeunload',prevent); return()=>window.removeEventListener('beforeunload',prevent)},[dirty])
 const change=(key:keyof ConnectionDraft,value:string)=>{
  setDraft(current=>({...current,[key]:value}))
  // The catalogue belongs to the gateway. Keep it while choosing a model;
  // clearing it for other edits avoids showing results from a changed connection.
  if (key !== 'model') setProbe(undefined)
  if(key==='context_window'||key==='max_tokens') setFieldErrors(current=>{
   if(!(key in current)) return current
   const next={...current}; delete next[key]; return next
  })
 }
 function acceptSaved(next: SettingsView) {
  const saved = connectionDraft(next)
  baseline.current = saved
  setDraft(saved)
  settings.setData(next)
 }
 async function save(){
  if(lock.current || !dirty)return
  // Validate before the request: the API's rule is the same, but the operator
  // needs to be told which field is wrong, and that field takes the focus.
  const problems=connectionProblems(draft)
  if(problems.length){
   const next: Partial<Record<ConnectionNumberField,string>>={}
   for(const problem of problems) next[problem.field]=problem.message
   setFieldErrors(next); setError('请修正标出的字段后再保存。')
   ;(problems[0].field==='context_window'?contextRef:maxTokensRef).current?.focus()
   return
  }
  lock.current=true;setBusy('save');setError('');setApply(null);setProbe(undefined);setFieldErrors({})
  try{
   const body={...draft,context_window:Number(draft.context_window||0),max_tokens:Number(draft.max_tokens||0)}
   if(!Number.isSafeInteger(body.context_window)||!Number.isSafeInteger(body.max_tokens)||body.context_window<0||body.max_tokens<0)throw Error('上下文窗口与输出上限必须是非负整数')
   const next=await api.saveSettings(body)
   acceptSaved(next)
   setApply(settingsApplyNotice(next))
  } catch (e) {
   const message = e instanceof Error ? e.message : String(e)
   if (e instanceof ApiError && e.code === 'settings_saved_apply_failed' &&
       e.body && typeof e.body === 'object' && 'settings' in e.body) {
    acceptSaved(e.body.settings as SettingsView)
    setApply({ tone: 'warning', text: message })
   } else if (e instanceof ApiError && e.status < 500) {
    setError(message)
   } else {
    // Older servers and interrupted responses may have written to disk before
    // the error reached the browser. Read back the saved value without retrying.
    try {
     settings.setData(await api.settings())
     setError(`应用状态未确认，下方显示服务器当前保存的模型。 ${message}`)
    } catch {
     setError(`未能确认保存结果。 ${message}`)
    }
   }
  } finally {
   lock.current = false
   setBusy(null)
  }
 }
 async function check(){
  if(lock.current)return
  lock.current=true;setBusy('check');setError('');setProbe(undefined)
  // A failed probe is an answer (the server reports it as 200 with available
  // false), so it is rendered as a result rather than thrown as a transport
  // error; only a real HTTP failure reaches the catch.
  try{setProbe(await api.checkModel())}catch(e){setError(e instanceof Error?e.message:String(e))}finally{lock.current=false;setBusy(null)}
 }
 const describedBy = (name: 'base_url'|'model'|'api_key'|'provider') => (name==='api_key' ? id('api-key-hint') : id(`${name}-hint`))
 const numberDescribedBy = (field: ConnectionNumberField) =>
  [id(`${field}-hint`), fieldErrors[field] ? id(`${field}-error`) : ''].filter(Boolean).join(' ')
 const catalogue = [...new Set(probe?.models)]
 const probeSummary = probe && checkProbeSummary(probe)
 return <Panel title="模型网关" extra={
  settings.data && <Badge variant={settings.data.configured ? 'success' : 'muted'} size="sm">{settings.data.configured ? '已配置' : '未配置'}</Badge>
 } bodyClassName="@container space-y-4">
  <p className="text-body text-ink-2">指挥官、执行者和复核共用此连接。模型路由、回退与密钥轮换由网关管理。</p>
  {settings.error && <Callout tone="destructive">{settings.error}</Callout>}
  {/* The form exists for Enter-to-save and for the field grouping; noValidate
      keeps the custom error text (with field ids and focus routing) in charge. */}
  <form noValidate onSubmit={(event) => { event.preventDefault(); void save() }} className="space-y-4">
   <fieldset disabled={!!busy || settings.loading} className="grid gap-4 @md:grid-cols-2">
    <SettingsField id={id('base_url')} label="接口地址" hintID={id('base_url-hint')} hint="填写网关 API 的根地址。">
     <Input id={id('base_url')} aria-label="接口地址" aria-describedby={describedBy('base_url')} type="text" inputMode="url" autoComplete="url" spellCheck={false} autoCapitalize="none" enterKeyHint="next" value={draft.base_url} onChange={e=>change('base_url',e.target.value)} placeholder="http://127.0.0.1:4000/v1" />
    </SettingsField>
    <SettingsField id={id('model')} label="模型名称" hintID={id('model-hint')} hint="填写网关提供的模型名称。">
     <Input id={id('model')} aria-label="模型名称" aria-describedby={describedBy('model')} type="text" inputMode="text" autoComplete="off" spellCheck={false} autoCapitalize="none" enterKeyHint="next" value={draft.model} onChange={e=>change('model',e.target.value)} placeholder="网关暴露的模型名称" />
    </SettingsField>
    <SettingsField id={id('api_key')} label="API Key" hintID={id('api-key-hint')} hint={settings.data?.api_key_set ? '留空保留已保存的密钥。' : '填写网关提供的 API Key。'}>
     <div className="flex items-center gap-1.5">
      <Input id={id('api_key')} aria-label="API Key" aria-describedby={describedBy('api_key')} type={revealKey?'text':'password'} autoComplete="new-password" spellCheck={false} autoCapitalize="none" enterKeyHint="next" value={draft.api_key} onChange={e=>change('api_key',e.target.value)} />
      <Button type="button" size="icon" variant="outline" className="shrink-0" aria-pressed={revealKey} aria-label={revealKey?'隐藏 API Key':'显示 API Key'} title={revealKey?'隐藏 API Key':'显示 API Key'} onClick={()=>setRevealKey(current=>!current)}>
       {revealKey?<EyeOff className="h-4 w-4" aria-hidden="true" />:<Eye className="h-4 w-4" aria-hidden="true" />}
      </Button>
     </div>
    </SettingsField>
    <SettingsField id={id('provider')} label="协议" hintID={id('provider-hint')} hint="网关的接口协议：openai / anthropic。">
     <Input id={id('provider')} aria-label="协议" aria-describedby={describedBy('provider')} type="text" inputMode="text" autoComplete="off" spellCheck={false} autoCapitalize="none" enterKeyHint="next" value={draft.provider} onChange={e=>change('provider',e.target.value)} placeholder="openai / anthropic" />
    </SettingsField>
    <SettingsField id={id('context_window')} label="上下文窗口（可选）" hintID={id('context_window-hint')} hint="留空交给网关；填写时必须是非负整数。" errorID={id('context_window-error')} error={fieldErrors.context_window}>
     <Input id={id('context_window')} ref={contextRef} aria-label="上下文窗口" aria-describedby={numberDescribedBy('context_window')} aria-invalid={fieldErrors.context_window?true:undefined} data-user-invalid={(fieldErrors.context_window?true:undefined) ? 'true' : undefined} type="number" min={0} step={1} inputMode="numeric" enterKeyHint="next" value={draft.context_window} onChange={e=>change('context_window',e.target.value)} />
    </SettingsField>
    <SettingsField id={id('max_tokens')} label="输出上限（可选）" hintID={id('max_tokens-hint')} hint="留空交给网关；填写时必须是非负整数。" errorID={id('max_tokens-error')} error={fieldErrors.max_tokens}>
     <Input id={id('max_tokens')} ref={maxTokensRef} aria-label="输出上限" aria-describedby={numberDescribedBy('max_tokens')} aria-invalid={fieldErrors.max_tokens?true:undefined} data-user-invalid={(fieldErrors.max_tokens?true:undefined) ? 'true' : undefined} type="number" min={0} step={1} inputMode="numeric" enterKeyHint="done" value={draft.max_tokens} onChange={e=>change('max_tokens',e.target.value)} />
    </SettingsField>
   </fieldset>
   <button type="submit" tabIndex={-1} aria-hidden="true" className="hidden" />
  </form>
  <div aria-live="polite" className="space-y-2">
   {error && <Callout tone="destructive">{error}</Callout>}
   {apply && <Callout tone={apply.tone}>{apply.text}</Callout>}
   {probeSummary && <Callout tone={probeSummary.tone}><span className="saker-break">{probeSummary.text}</span></Callout>}
  </div>
  {catalogue.length > 0 && (
   <section aria-labelledby={id('catalogue')} className="space-y-2 rounded-lg border border-hairline bg-surface-2/40 p-3">
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
     <p id={id('catalogue')} className="text-ui font-medium text-ink">可用模型（{catalogue.length}）</p>
     <p className="text-caption text-ink-3">点选即可填入模型名称。保存后可检测新模型。</p>
    </div>
    <ul role="list" className="saker-scroll max-h-48 space-y-1.5 overflow-y-auto p-1">
     {catalogue.map(model => (
      <li key={model}>
       <Button
        type="button"
        variant={draft.model === model ? 'soft' : 'outline'}
        aria-pressed={draft.model === model}
        className="h-auto min-h-8 w-full justify-start whitespace-normal px-2.5 py-1.5 text-left font-mono text-caption"
        onClick={() => change('model', model)}
       >
        {draft.model === model && <Check aria-hidden="true" className="size-3.5 shrink-0" />}
        <span className="saker-break">{model}</span>
       </Button>
      </li>
     ))}
    </ul>
   </section>
  )}
  {settings.data && (
   <p className="text-caption text-ink-3">已保存的模型 <strong className="saker-break font-mono font-medium text-ink-2">{settings.data.model || '未配置'}</strong></p>
  )}
  <div className="saker-settings-actions flex flex-wrap items-center gap-2">
   <Button onClick={() => void save()} disabled={!!busy || !dirty} aria-busy={busy === 'save'}>
    {busy === 'save' && <Spinner size="sm" />}
    保存并应用
   </Button>
   <Button
    variant="outline"
    onClick={() => void check()}
    disabled={!!busy || !!dirty || !settings.data?.configured}
    title={dirty ? '保存修改后可以检测连接' : undefined}
    aria-busy={busy === 'check'}
   >
    {busy === 'check' && <Spinner size="sm" />}
    {busy === 'check' ? '检测中…' : '检测连接'}
   </Button>
   {dirty && <span className="text-caption text-ink-3">有未保存的修改</span>}
  </div>
 </Panel>
}

/**
 * 扩展工具: the scan node's product sections (MCP servers, Tavily search keys).
 *
 * They are edited as JSON because the harness shape is nested (command/url,
 * args, env, headers, timeouts) and a row editor would still serialise it. The
 * secret semantics are the server's: env/header values read back as "***" and a
 * save keeps the stored value; a typed Tavily key is write-only (only has_key /
 * count are ever returned). Product sections are read when the scan starts, so
 * a changed save says "needs restart" instead of claiming it is already live.
 */
export function ExtensionsPanel({ settings }: { settings: AsyncState<SettingsView> }) {
 const data = settings.data
 const baseline = useRef<string>()
 const [draft,setDraft] = useState('')
 const [touched,setTouched] = useState(false)
 const [searchKey,setSearchKey] = useState('')
 const [revealKey,setRevealKey] = useState(false)
 const [clearKey,setClearKey] = useState(false)
 const [busy,setBusy] = useState(false)
 const [error,setError] = useState('')
 const [warnings,setWarnings] = useState<string[]>([])
 const [apply,setApply] = useState<{tone:'success'|'warning'|'info';text:string}|null>(null)
 const lock = useRef(false)
 const uid = useId()
 const jsonID = `${uid}-mcp`
 const jsonHintID = `${uid}-mcp-hint`
 const jsonErrorID = `${uid}-mcp-error`
 const keyID = `${uid}-tavily`
 const keyHintID = `${uid}-tavily-hint`
 // Stored (masked) count, not the draft's: it reports what the scan will load.
 const mcpCount = Object.keys(data?.mcp_servers ?? {}).length
 useEffect(()=>{
  if(!data) return
  const next = mcpServersDraftText(data.mcp_servers)
  const old = baseline.current
  baseline.current = next
  setDraft(current => (old === undefined || current === old) ? next : current)
 },[data])
 const dirty = !!data && (touched || (baseline.current !== undefined && draft !== baseline.current) || searchKey.trim() !== '' || clearKey)
 useEffect(()=>{if(!dirty)return; const prevent=(e:BeforeUnloadEvent)=>{e.preventDefault();e.returnValue=''}; window.addEventListener('beforeunload',prevent); return()=>window.removeEventListener('beforeunload',prevent)},[dirty])
 async function save(){
  if(lock.current || !data || !dirty) return
  const parsed = parseMCPServersDraft(draft)
  setWarnings(parsed.warnings)
  if(parsed.error || parsed.value == null){
   setError(parsed.error ?? 'MCP 服务器配置无效')
   setApply(null)
   return
  }
  lock.current=true;setBusy(true);setError('');setApply(null)
  try{
   // A PUT applies the whole request view, so the model half must ride along:
   // sending only the product fields would clear the gateway connection.
   const body: Partial<SettingsView> = { ...settingsModelPayload(data), mcp_servers: parsed.value }
   if(searchKey.trim()) body.search_tavily_key = searchKey.trim()
   if(clearKey) body.clear_search_key = true
   const next = await api.saveSettings(body)
   settings.setData(next)
   baseline.current = mcpServersDraftText(next.mcp_servers)
   setDraft(baseline.current)
   setTouched(false); setSearchKey(''); setClearKey(false)
   setApply(settingsApplyNotice(next))
  }catch(e){setError(e instanceof Error?e.message:String(e))}finally{lock.current=false;setBusy(false)}
 }
 return <Panel title="扩展工具" extra={<Badge variant="outline" size="sm">实例级</Badge>} bodyClassName="@container space-y-4">
  <p className="text-body text-ink-2">
   配置 scan 节点上的 MCP 服务器和 Tavily 搜索。节点启动时读取配置，保存后会提示是否需要重启。
  </p>
  {/* Visibility model: MCP is a process-level command registry on the scan
      node. It is not a per-role switch, so the commander and executor sessions
      on the same node see the same servers. */}
  <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-hairline bg-surface-2/30 px-3 py-2 text-caption text-ink-3">
   <Badge size="sm" variant={mcpCount > 0 ? 'success' : 'muted'}>
    {mcpCount > 0 ? `已配置 ${mcpCount} 个 MCP 服务器` : 'MCP 未启用'}
   </Badge>
   <span className="min-w-0 basis-60 grow">
    同一 scan 节点上的指挥官和执行者共享这些服务器。
   </span>
  </div>
  {data?.product_error && <Callout tone="destructive">已保存的产品配置无法解析：<span className="saker-break">{data.product_error}</span></Callout>}
  {settings.error && <Callout tone="destructive">读取设置失败：{settings.error}</Callout>}
  <form noValidate onSubmit={(event) => { event.preventDefault(); void save() }} className="space-y-4">
   <fieldset disabled={busy||settings.loading||!data} className="space-y-4">
    <div className="block space-y-1.5">
     <label htmlFor={jsonID} className="block text-[12px] font-medium leading-4 text-ink-2">MCP 服务器（JSON）</label>
     <Textarea
      id={jsonID}
      aria-label="MCP 服务器 JSON"
      aria-describedby={[jsonHintID, error ? jsonErrorID : ''].filter(Boolean).join(' ')}
      aria-invalid={error ? true : undefined}
      data-user-invalid={error ? 'true' : undefined}
      rows={8}
      spellCheck={false}
      autoComplete="off"
      className="font-mono text-caption"
      value={draft}
      onChange={(e) => { setDraft(e.target.value); setTouched(true); setError('') }}
      placeholder={'{\n  "playwright": {\n    "command": "npx",\n    "args": ["-y", "@playwright/mcp"],\n    "env": { "TOKEN": "***" }\n  }\n}'}
     />
     {error ? (
      <span id={jsonErrorID} className="flex items-start gap-1 text-[11px] leading-4 text-destructive">
       <TriangleAlert aria-hidden="true" className="mt-0.5 h-3 w-3 shrink-0" />
       <span className="saker-break">{error}</span>
      </span>
     ) : (
      <span id={jsonHintID} className="block text-[11px] leading-4 text-ink-3">
       别名可用字母、数字、- 和 _。每个服务器填写 command 或 url 中的一项。留空或填写 {'{}'} 会清空配置，保留 *** 会沿用已保存的密钥。
      </span>
     )}
    </div>
    {warnings.length > 0 && (
     <Callout tone="warning">
      <ul role="list" className="space-y-0.5">
       {warnings.map((warning) => <li key={warning} className="saker-break">{warning}</li>)}
      </ul>
     </Callout>
    )}
    <div className="block space-y-1.5">
     <label htmlFor={keyID} className="block text-[12px] font-medium leading-4 text-ink-2">Tavily 搜索 Key</label>
     <div className="flex flex-wrap gap-1.5">
      <div className="flex min-w-0 flex-1 gap-1.5">
       <Input
        id={keyID}
        aria-label="Tavily 搜索 Key"
        aria-describedby={keyHintID}
        type={revealKey ? 'text' : 'password'}
        autoComplete="new-password"
        spellCheck={false}
        autoCapitalize="none"
        enterKeyHint="next"
        value={searchKey}
        onChange={(e) => { setSearchKey(e.target.value); if (e.target.value) setClearKey(false) }}
        placeholder={data?.search_has_key ? '留空保留已保存的 key' : '填写 Tavily key（多个用逗号分隔）'}
        className="min-w-0 flex-1"
       />
       <Button
        type="button"
        size="icon-sm"
        variant="ghost"
        aria-pressed={revealKey}
        aria-label={revealKey ? '隐藏 Tavily Key' : '显示新输入的 Tavily Key'}
        title={revealKey ? '隐藏 Tavily Key' : '显示新输入的 Tavily Key'}
        onClick={() => setRevealKey((current) => !current)}
       >
        {revealKey ? <EyeOff className="h-4 w-4" aria-hidden="true" /> : <Eye className="h-4 w-4" aria-hidden="true" />}
       </Button>
      </div>
      <Button
       type="button"
       size="sm"
       variant={clearKey ? 'destructive' : 'outline'}
       disabled={!data?.search_has_key}
       aria-pressed={clearKey}
       onClick={() => { setClearKey((current) => !current); setSearchKey('') }}
      >
       <KeyRound className="h-3.5 w-3.5" aria-hidden="true" /> {clearKey ? '取消清除' : '清除已保存 Key'}
      </Button>
     </div>
     <span id={keyHintID} className="block text-[11px] leading-4 text-ink-3">
      {clearKey ? '保存后会清除 Tavily Key。' : `${searchKeyStatus(data ?? { search_has_key: false })}。留空会保留当前密钥。`}
     </span>
    </div>
   </fieldset>
   <button type="submit" tabIndex={-1} aria-hidden="true" className="hidden" />
  </form>
  <div aria-live="polite" className="space-y-2">
   {apply && <Callout tone={apply.tone}>{apply.text}</Callout>}
   {error && <Callout tone="destructive">{error}</Callout>}
  </div>
  <div className="saker-settings-actions flex flex-wrap items-center gap-2">
   <Button onClick={() => void save()} disabled={busy||!data||!dirty}>
    {busy && <Spinner size="sm" />} 保存扩展工具
   </Button>
   <Button type="button" variant="outline" onClick={() => settings.reload()} disabled={busy||settings.refreshing}>
    刷新配置
   </Button>
  </div>
 </Panel>
}

/**
 * 流量捕获：the scan node's capture-directory retention cap.
 *
 * One number for the whole node — the capture directory is shared by every
 * project on that machine, so this is not a per-project body quota and saker
 * keeps no project-level copy of capture bodies. The wire field is bytes
 * (`traffic_body_retention_bytes`); the operator edits MiB. 0/absent means the
 * generated default applies and is shown as "默认 2 GiB（未显式设置）", never 0.
 * The runtime yaml is read when the scan starts, so a changed value needs the
 * scan node restart the save response reports.
 */
export function TrafficRetentionPanel({ settings }: { settings: AsyncState<SettingsView> }) {
 const data = settings.data
 const [draft,setDraft] = useState('')
 const [busy,setBusy] = useState(false)
 const [error,setError] = useState('')
 const [fieldError,setFieldError] = useState('')
 const [apply,setApply] = useState<{tone:'success'|'warning'|'info';text:string}|null>(null)
 const baseline = useRef<string>()
 const lock = useRef(false)
 const uid = useId()
 const inputID = `${uid}-retention`
 const hintID = `${uid}-retention-hint`
 const errorID = `${uid}-retention-error`
 const inputRef = useRef<HTMLInputElement>(null)
 useEffect(()=>{
  if(!data) return
  const next = retentionDraft(data.traffic_body_retention_bytes)
  const old = baseline.current
  baseline.current = next
  setDraft(current => (old === undefined || current === old) ? next : current)
 },[data])
 const dirty = !!data && baseline.current !== undefined && draft !== baseline.current
 async function save(){
  if(lock.current || !data || !dirty) return
  const problem = retentionProblem(draft)
  if(problem){
   setFieldError(problem); setError(''); setApply(null)
   inputRef.current?.focus()
   return
  }
  lock.current=true;setBusy(true);setError('');setFieldError('');setApply(null)
  try{
   // A PUT applies the whole settings view, so the model half rides along; only
   // this one traffic field is added on top of it.
   const next = await api.saveSettings({
    ...settingsModelPayload(data),
    traffic_body_retention_bytes: retentionBytesFromDraft(draft),
   })
   settings.setData(next)
   baseline.current = retentionDraft(next.traffic_body_retention_bytes)
   setDraft(baseline.current)
   setApply(settingsApplyNotice(next))
  }catch(e){setError(e instanceof Error?e.message:String(e))}finally{lock.current=false;setBusy(false)}
 }
 const storedBytes = data?.traffic_body_retention_bytes
 return <Panel
  title="流量捕获"
  extra={<Badge variant="outline" size="sm">scan 节点 · 所有项目共享</Badge>}
  bodyClassName="@container space-y-3"
  data-testid="traffic-retention"
 >
  <p className="text-body text-ink-2">
   设置 scan 节点 <code className="rounded bg-surface-2 px-1 font-mono text-caption">.http</code> 捕获目录的总容量。该节点上的项目共享这个上限。
  </p>
  <form noValidate onSubmit={(event) => { event.preventDefault(); void save() }} className="grid min-w-0 gap-3 @md:grid-cols-[minmax(0,280px)_auto] @md:items-start">
   <div className="block space-y-1.5">
    <label htmlFor={inputID} className="block text-ui font-medium text-ink-2">捕获保留上限（MiB）</label>
    <div className="flex items-center gap-1.5">
     <Input
      id={inputID}
      ref={inputRef}
      aria-label="捕获保留上限（MiB）"
      aria-describedby={[hintID, fieldError ? errorID : ''].filter(Boolean).join(' ')}
      aria-invalid={fieldError ? true : undefined}
      data-user-invalid={fieldError ? 'true' : undefined}
      type="number"
      min={TRAFFIC_RETENTION_MIN_MIB}
      max={TRAFFIC_RETENTION_MAX_MIB}
      step={1}
      inputMode="numeric"
      enterKeyHint="done"
      value={draft}
      onChange={(e) => { setDraft(e.target.value); setFieldError('') }}
      placeholder="2048"
      disabled={busy || settings.loading || !data}
      className="min-w-0 flex-1"
     />
     <span className="shrink-0 text-[11px] text-ink-3">MiB</span>
     <span className="saker-nums shrink-0 font-mono text-[11px] text-ink-3">{retentionStatusText(storedBytes)}</span>
    </div>
    {fieldError ? (
     <span id={errorID} className="flex items-start gap-1 text-[11px] leading-4 text-destructive">
      <TriangleAlert aria-hidden="true" className="mt-0.5 h-3 w-3 shrink-0" />
      <span>{fieldError}</span>
     </span>
    ) : (
     <span id={hintID} className="block text-[11px] leading-4 text-ink-3">
      范围为 {TRAFFIC_RETENTION_MIN_MIB}–1,048,576 MiB。留空恢复默认值（2 GiB）。
     </span>
    )}
   </div>
   <div className="flex flex-wrap items-center gap-2 @md:pt-[26px]">
    <Button
     type="button"
     size="sm"
     variant="secondary"
     onClick={() => void save()}
     disabled={busy || !data || !dirty}
     aria-busy={busy}
     aria-label="保存捕获保留上限"
    >
     {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
     {busy ? '保存中…' : '保存保留上限'}
    </Button>
   </div>
  </form>
  <p className="text-[11px] leading-relaxed text-ink-3">
   保存后需重启 scan 节点生效。单条正文默认上限 8 MiB，可通过 <code className="rounded bg-surface-2 px-1 font-mono text-[11px]">traffic.body_max_bytes</code> 调整。
  </p>
  <div aria-live="polite" className="space-y-2">
   {apply && <Callout tone={apply.tone}>{apply.text}</Callout>}
   {error && <Callout tone="destructive">{error}</Callout>}
   {settings.error && <Callout tone="warning">读取设置失败：{settings.error}</Callout>}
  </div>
 </Panel>
}

/**
 * The instance default for reviews. It shares the runtime-settings endpoint with
 * the auxiliary gate: an omitted field keeps its stored value, so saving one panel
 * never overwrites the other. `--no-verify` only seeds the initial value; once
 * this is saved the file wins. Per-project overrides still take precedence — this
 * panel decides what new, reopened and inheriting projects do, never what an
 * explicitly pinned project does. The default is read when a project attaches, so
 * a running project keeps the switch it started with and is changed on its card.
 */
export function ReviewDefaultPanel({ runtime }: { runtime: AsyncState<RuntimeSettings> }) {
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const [notice, setNotice] = useState('')
 const value = runtime.data?.reviews_disabled
 const known = value !== undefined
 const on = value === false

 async function toggle(next: boolean) {
  if (busy || !known) return
  setBusy(true); setError(''); setNotice('')
  try {
   const saved = await api.saveRuntimeSettings({ reviews_disabled: !next })
   runtime.setData(saved)
   setNotice(next ? '已保存，默认开启复核。' : '已保存，默认关闭复核。')
  } catch (e) {
   setError(`保存失败：${e instanceof Error ? e.message : String(e)}`)
  } finally {
   setBusy(false)
  }
 }

 return (
  <Panel
   title="复核默认值"
   extra={<Badge variant="outline" size="sm">实例级</Badge>}
   bodyClassName="space-y-3"
  >
   <div className="flex items-center gap-3 rounded-lg border border-hairline bg-surface-2/40 p-3">
    <Switch
     id="review-default"
     checked={on}
     disabled={busy || !known || runtime.loading}
     onCheckedChange={(next) => void toggle(next)}
    />
    <label htmlFor="review-default" className="min-w-0 flex-1 cursor-pointer text-ui text-ink-2">
     <span className="block font-medium text-ink">
      新项目的复核默认值：{known ? (on ? '开启' : '关闭') : '读取中…'}
     </span>
    </label>
   </div>
   <p className="text-caption text-ink-3">
    跟随实例的项目在启动时读取默认值。要立即调整运行中项目的复核，请在「项目设置」中操作。
   </p>
   <div aria-live="polite" className="space-y-2">
    {notice && <Callout tone="success">{notice}</Callout>}
    {error && <Callout tone="destructive">{error}</Callout>}
    {runtime.error && <Callout tone="warning">读取运行时设置失败：{runtime.error}</Callout>}
   </div>
  </Panel>
 )
}

function RuntimeConcurrencyPanel({ runtime }: { runtime: AsyncState<RuntimeSettings> }) {
 const [draft, setDraft] = useState('')
 const baseline = useRef<string>()
 const lock = useRef(false)
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const [notice, setNotice] = useState('')
 const uid = useId()
 const inputRef = useRef<HTMLInputElement>(null)
 const inputID = `${uid}-concurrency`
 const hintID = `${uid}-concurrency-hint`
 const errorID = `${uid}-concurrency-error`

 const current = runtime.data?.auxiliary_concurrency
 const max = runtime.data?.max_auxiliary_concurrency ?? 8
 const dirty = runtime.data != null && draft !== String(current)
 useEffect(() => {
  if (current === undefined) return
  const next = String(current)
  const old = baseline.current
  baseline.current = next
  setDraft(value => old === undefined || value === old ? next : value)
 }, [current])

 async function save() {
  if (lock.current || !dirty) return
  const problem = concurrencyProblem(draft, max)
  if (problem) {
   setError(problem); setNotice('')
   inputRef.current?.focus()
   return
  }
  lock.current = true
  setBusy(true); setError(''); setNotice('')
  try {
   const next = await api.saveRuntimeSettings({ auxiliary_concurrency: Number(draft) })
   runtime.setData(next)
   setDraft(String(next.auxiliary_concurrency))
   setNotice('已保存。新提交的复核与报告按此上限调度。')
  } catch (e) {
   setError(e instanceof Error ? e.message : String(e))
  } finally {
   lock.current = false
   setBusy(false)
  }
 }

 return (
  <Panel
   title="复核与报告并发"
   extra={<Badge variant="outline" size="sm">实例级</Badge>}
   bodyClassName="@container space-y-3"
  >
   <form noValidate onSubmit={(event) => { event.preventDefault(); void save() }} className="grid min-w-0 gap-3 @md:grid-cols-[minmax(0,240px)_auto] @md:items-start">
    <SettingsField
     id={inputID}
     label="同时运行的复核/报告数"
     hintID={hintID}
     hint={runtime.loading ? '加载中…' : `当前 ${current ?? '…'}，必填，可设 1–${max}`}
     errorID={errorID}
     error={error}
    >
     <Input
      id={inputID}
      ref={inputRef}
      type="number"
      min={1}
      max={max}
      step={1}
      required
      inputMode="numeric"
      enterKeyHint="done"
      value={draft}
      onChange={e => { setDraft(e.target.value); setError('') }}
      disabled={busy || !runtime.data}
      aria-label="复核与报告并发"
      aria-invalid={error ? true : undefined} data-user-invalid={(error ? true : undefined) ? 'true' : undefined}
      aria-describedby={[hintID, error ? errorID : ''].filter(Boolean).join(' ')}
     />
    </SettingsField>
    <div className="flex flex-wrap items-center gap-2 @md:pt-[26px]">
     <Button type="button" size="sm" variant="secondary" onClick={() => void save()} disabled={busy || !runtime.data || !dirty} aria-busy={busy}>
      {busy && <Spinner size="sm" className="h-3.5 w-3.5" />}
      {busy ? '保存中…' : '保存并发'}
     </Button>
     {runtime.error && <Button type="button" size="xs" variant="outline" onClick={runtime.reload}>重试读取</Button>}
    </div>
   </form>
   <p className="text-caption text-ink-3">
    复核与报告共享此上限。默认一次运行一项，执行者使用各项目自己的并发设置。
   </p>
   <div aria-live="polite" className="space-y-2">
    {notice && <Callout tone="success">{notice}</Callout>}
    {runtime.error && <Callout tone="destructive">读取复核并发失败：{runtime.error}</Callout>}
   </div>
  </Panel>
 )
}
