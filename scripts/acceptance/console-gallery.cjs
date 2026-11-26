// Reproducible console screenshots from synthetic responses and the real bundle.
// Nothing is committed: shots land in .build/ui-qa/.
// No console, harness, saved project, model API or credential is accessed.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const assetTypes = { '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' };
const web = process.argv.includes('--web')
  ? path.resolve(root, process.argv[process.argv.indexOf('--web') + 1])
  : path.join(root, 'internal/web/static');
const defaultOut = path.join(root, '.build/ui-qa');
fs.mkdirSync(defaultOut, { recursive: true });

// Optional capture parameters (defaults keep the original gallery run intact):
//   --pages overview,state,runs   which pages to shoot
//   --viewport 1440x900|390x844   one viewport for every selected page
//   --theme dark|light            class-based theme (.dark) after render
//   --out .build/ui-qa            output directory
const argv = process.argv.slice(2);
const argValue = (name) => {
  const i = argv.indexOf('--' + name);
  return i >= 0 ? argv[i + 1] : undefined;
};
const out = argValue('out') ? path.resolve(root, argValue('out')) : defaultOut;
fs.mkdirSync(out, { recursive: true });
const viewportArg = argValue('viewport');
const themeArg = argValue('theme');
const pagesArg = argValue('pages');
// --search types into the graph filter box before the shot, so a capture can
// show how search matches and the selected focus coexist.
// --hub swaps the graph fixture for a synthetic hub snapshot: the project brief
// is the premise of every root direction, so its chain closure is the whole map.
// That is the case that must open on the main path instead of lighting 200 of
// 301 nodes. Offline sample data only.
const hubArg = argv.includes('--hub');
// --expand opens every <details> before the shot (tool failure reasons, legend).
const expandArg = argv.includes('--expand');
const searchArg = argValue('search');
// --chain path clicks 只看主路径 before the shot (the single-chain view); the
// button lives in the strip on a wide map and in the node panel on a narrow one.
const chainArg = argValue('chain');
// --timeline 40 scrubs the time axis to 40% before the shot, so a still capture
// can show what "not revealed yet" looks like.
const timelineArg = argValue('timeline');
// --allow-overflow keeps the capture going when a page is expected to overflow
// (it is how a before/after pair is taken of the broken vs fixed layout).
const allowOverflow = argv.includes('--allow-overflow');
// --quarantine makes the projects index answer a boot that set aside an
// unreadable vuln index, so the standing notice can be captured. Synthetic
// paths only: no real data root, no real file. Healthy boots answer `null`.
const quarantineArg = argv.includes('--quarantine');
// --pending swaps the empty operator inbox for one with rows and one unreadable
// category, so a capture can show the badge's lower bound ("4+"), the card rows
// and the click-through to a record. Synthetic only.
const pendingArg = argv.includes('--pending');
// --real-counts swaps the board counters for a pre-run snapshot with the same
// digits a live console shows (runs, failures, pending directions, held, parked,
// goals). Synthetic only: no project id, no real ledger, no real counts.
const realCountsArg = argv.includes('--real-counts');
// --two-goals makes the goals route return two open goals, so the 约束 page can
// be captured showing that only the current goal is edited there and the other
// one is announced rather than silently edited. Synthetic only.
const twoGoalsArg = argv.includes('--two-goals');
// --pause <kind> renders a paused project whose persisted cause is that code,
// with the same sentence the control plane puts in status.reason. --append-goal
// <kind> additionally answers the synthetic POST /api/project/append-goal with
// that cause still in effect, so the dialog's 「项目仍在暂停：…」 line can be
// captured. Offline fixtures only: no live instance, no real project data.
const pauseArg = argValue('pause');
const appendGoalArg = argValue('append-goal');
const PAUSE_COPY = {
  restart_pause: '控制面重启：对账后自动恢复；若仍暂停，确认现场后点「继续项目」',
  unrecorded: '暂停原因未记录：确认现场后点「继续项目」',
  manual_pause: '操作员暂停',
};
if (pauseArg && !PAUSE_COPY[pauseArg]) throw Error('bad --pause: ' + pauseArg);
if (appendGoalArg && !PAUSE_COPY[appendGoalArg]) throw Error('bad --append-goal: ' + appendGoalArg);
// --append-resumed answers the append the way the server does once a restart
// pause is in the resumable set: it clears the pause, so the dialog shows
// 已恢复派发 instead of 项目仍在暂停. Only a restart pause models that.
const appendResumedArg = argv.includes('--append-resumed');
if (appendResumedArg) {
  if (!appendGoalArg) throw Error('--append-resumed needs --append-goal');
  if (appendGoalArg !== 'restart_pause') throw Error('--append-resumed only models a restart pause');
}
// The dialog names the machine code, not the long status sentence.
const PAUSE_LABEL = {
  restart_pause: '平台重启后自动恢复未完成',
  unrecorded: '暂停原因未记录',
  manual_pause: '人工暂停',
};
// --probe <selector> prints one element's innerText after render (debugging aid).
const probeArg = argValue('probe');
// --click <selector> clicks one element after the page is ready (opening a
// report preview or a disclosure) so the shot shows the expanded state.
const clickArg = argValue('click');
// --partial-fact answers /api/facts/<id> without the evidence list: the shape
// change (a 404 wrapped as 200 + {}, or an older server) that used to crash the
// node panel. The panel must say "没读全" instead.
const partialFactArg = argv.includes('--partial-fact');
// --views opens the graph's 视图 popover before the shot (layout names + help).
const viewsArg = argv.includes('--views');
// --eval <expression> prints the JSON value of one page expression (debugging aid).
const probeJS = argValue('eval');
const [viewportWidth, viewportHeight] = (viewportArg || '1440x1000').split('x').map(Number);
if (!Number.isFinite(viewportWidth) || !Number.isFinite(viewportHeight)) throw Error('bad --viewport: ' + viewportArg);
if (themeArg && themeArg !== 'dark' && themeArg !== 'light') throw Error('bad --theme: ' + themeArg);

// Page table used by both the original gallery and the parameterized runs.
const PAGES = {
  fleet: { route: '/g/?view=fleet', expected: '运行时间线', name: 'fleet.png', height: 900 },
  settings: { route: '/g/?view=settings', expected: '统一模型连接', name: 'models.png', height: 1000 },
  workers: { route: '/p/fixture/?view=runs&tab=workers', expected: '核对账户访问边界', name: 'workers.png', height: 1000 },
  projects: { route: '/g/?view=projects', expected: '应用安全评估示例', name: 'projects.png', height: 640 },
  overview: { route: '/p/fixture/?view=overview', expected: '项目状态', name: 'overview.png', height: 900 },
  state: { route: '/p/fixture/?view=state', expected: '核对账户访问边界', name: 'state.png', height: 900 },
  runs: { route: '/p/fixture/?view=runs', expected: '事件流', name: 'runs.png', height: 900 },
  messages: { route: '/p/fixture/?view=messages', expected: '事实质量修正已完成', name: 'messages.png', height: 900 },
  graph: { route: '/p/fixture/?view=graph#f:f_a', expected: '事实', name: 'graph-node.png', height: 900 },
  'graph-map': { route: '/p/fixture/?view=graph', expected: '快照已载', name: 'graph-map.png', height: 900 },
  // The brief itself: with --hub its closure is the whole synthetic map, which is
  // the default-main-path case.
  'graph-brief': { route: '/p/fixture/?view=graph#p:fixture', expected: 'portal.example.test（离线示例）', name: 'graph-brief.png', height: 900 },
  // Tool statistics: twelve synthetic rows, so the capped list and the sort
  // controls are both exercised.
  toolbox: { route: '/p/fixture/?view=runtime&tab=toolbox', expected: '工具与能力', name: 'toolbox.png', height: 1000 },
  // Record-level inbox links: an authorization request on 访问 (`?id=`) and a
  // stale goal receipt on 状态 (`?goal=`). Both need --pending for the inbox data.
  'access-auth': { route: '/p/fixture/?view=access&id=req_fixture_1', expected: '授权请求', name: 'access-auth.png', height: 900 },
  // The access page itself: recent closed requests whose two states disagree.
  'access-recent': { route: '/p/fixture/?view=access', expected: '最近处理过的请求', name: 'access-recent.png', height: 900 },
  'state-goal': { route: '/p/fixture/?view=state&goal=g_access', expected: '目标验收回执', name: 'state-goal.png', height: 900 },
  // One direction whose premise is unavailable: 等待原因 must name the premise
  // ids, not hide them in a tooltip.
  'state-blocked': { route: '/p/fixture/?view=state&intent=in_review', expected: '复核访问控制结论', name: 'state-blocked.png', height: 1000 },
  docs: { route: '/p/fixture/?view=docs', expected: '起点', name: 'docs.png', height: 1000 },
  // The two review-failure shapes: a verdict that said no names its fact, an
  // interrupted review says retry. Neither may borrow the other's wording.
  'state-review-rejected': { route: '/p/fixture/?view=state&intent=in_review_rejected', expected: '复核判定不通过', name: 'state-review-rejected.png', height: 1000 },
  'state-review-unfinished': { route: '/p/fixture/?view=state&intent=in_review_unfinished', expected: '复核没跑完（可重试）', name: 'state-review-unfinished.png', height: 1000 },
  // The report trace: 预检 lists the facts no report cites, 结论行 carries its fact
  // id, and a fact has a record address of its own (`?view=graph&fact=`).
  reports: { route: '/p/fixture/?view=reports', expected: '报告列表', name: 'reports.png', height: 1000 },
  // One open report as a record: the trace's way back lands on this URL.
  'report-open': { route: '/p/fixture/?view=reports&report=rep_fixture_1', expected: '报告列表', name: 'report-open.png', height: 1000 },
  fact: { route: '/p/fixture/?view=graph&fact=f_a', expected: '断言与复核', name: 'fact-trace.png', height: 900 },
  // Same fact, answered without its evidence list (--partial-fact): the panel
  // must say 没读全 instead of crashing on an undefined list.
  'fact-partial': { route: '/p/fixture/?view=graph&fact=f_a', expected: '没读全', name: 'fact-partial.png', height: 900 },
};
const selectedPages = pagesArg ? pagesArg.split(',').map((s) => s.trim()).filter(Boolean) : ['settings', 'workers', 'projects'];
for (const id of selectedPages) if (!PAGES[id]) throw Error('unknown --pages entry: ' + id);
const profileDir = fs.mkdtempSync(path.join(defaultOut, 'gallery-chrome-'));
const chrome = spawn(process.env.SAKER_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
  '--user-data-dir=' + profileDir, 'about:blank',
], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const at = '2026-11-16T09:30:00+08:00';
const project = { id: 'fixture', name: '应用安全评估示例', created: at, paused: true,
  scope: 'portal.example.test（离线示例）',
  description: '在隔离示例环境中核对访问控制与证据完整性。',
  intents: 3, facts: 6, evidence: 4, running: 1, max_runs_effective: 3,
  // No override: this project follows the instance default (on).
  reviews_disabled: null, reviews_disabled_effective: true };
// Historical per-run model metadata (profile/resolved_profile) is not part of
// current runs; synthetic rows carry only what the console still shows today.
const workers = [
  { intent_id: 'in_access', participant_id: 'pt_in_access', title: '核对账户访问边界',
    status: 'running', running: true, can_kill: true, run_id: 'r_access',
    model: 'example-model', node: 'local', created_at: at,
    last_event_kind: 'tool_result', last_event_text: '已记录响应样本，正在核对授权范围与验收条件。', last_event_at: at },
  { intent_id: 'in_evidence', participant_id: 'pt_in_evidence', title: '整理本轮原始证据',
    status: 'waiting', running: false, can_kill: true, run_id: 'r_evidence',
    parent_intent_id: 'in_access', model: 'example-model', node: 'local', created_at: at,
    last_event_kind: 'handoff', last_event_text: '等待关联意图交付原始材料。', last_event_at: at },
  { intent_id: 'in_entry', participant_id: 'pt_in_entry', title: '确认示例环境入口',
    status: 'done', running: false, can_kill: false, run_id: 'r_entry',
    model: 'example-model', node: 'local', created_at: at,
    last_event_kind: 'turn_ended', last_event_text: '入口状态和来源材料已登记。', last_event_at: at },
];
// --long-name swaps the synthetic project name for a long one. The header caps
// the eyebrow at 16rem and truncates it; the 1024/768 wrap has to be checked on
// the worst case, not on the short fixture name. Offline sample text only.
if (argv.includes('--long-name')) {
  project.name = '示例项目·华东区域二期攻防演练与证据核验（离线演示数据）';
}

// Read-only fixtures for the three project pages added to the gallery. They are
// only requested by ?view=overview|state|runs and never by the original three
// screenshots, so the existing assertions keep the same inputs.
const fixtureIntents = [
  { id: 'in_access', title: '核对账户访问边界', status: 'running', attempts: 1, created_at: at, updated_at: at,
    description: '在隔离示例环境中核对账户权限边界。', node: 'local', doc: '示例任务书', notes: [] },
  { id: 'in_entry', title: '确认示例环境入口', status: 'done', attempts: 1, created_at: at, updated_at: at,
    description: '登记入口与来源材料。', node: 'local', notes: [] },
  { id: 'in_evidence', title: '整理本轮原始证据', status: 'waiting', attempts: 1, created_at: at, updated_at: at,
    description: '等待关联意图交付原始材料。', node: 'local', notes: [{ kind: 'retry', text: '补齐证据目录', at }] },
  { id: 'in_review', title: '复核访问控制结论', status: 'pending', attempts: 0, created_at: at, updated_at: at,
    description: '复核结论是否成立。', node: 'local', notes: [], from_fact_ids: ['f_premise'] },
  // Two review failures that must not read the same: one was decided ("no"), the
  // other never got a row because the verifier run died. Offline sample data.
  { id: 'in_review_rejected', title: '复核否定示例结论', status: 'review_failed', attempts: 1,
    created_at: at, updated_at: at, description: '复核判定结论证据不足。', node: 'local',
    notes: [{ kind: 'review', text: '示例复核：证据不足，结论需改写', at }], review_failed_facts: ['f_acc_2'] },
  { id: 'in_review_unfinished', title: '复核中断示例意图', status: 'review_failed', attempts: 1,
    created_at: at, updated_at: at, description: '复核 run 失败，没有写出任何 review 行。', node: 'local',
    notes: [{ kind: 'review', text: '复核未完成：示例拨号失败', at }], review_failed_facts: ['f_acc_3'] },
];
// The running example started 12 minutes before capture, so "quiet for 12m"
// is visible instead of a three-day-old fixture timestamp.
const recent = new Date(Date.now() - 12 * 60 * 1000).toISOString();
const fixtureRuns = [
  { id: 'r_access', intent_id: 'in_access', node: 'local', status: 'running', events: 12, declared_facts: 2,
    tokens: 1840, started_at: recent, session_id: 'session_access', model: 'example-model', provider: 'openai',
    purpose: '', warnings: [] },
  { id: 'r_entry', intent_id: 'in_entry', node: 'local', status: 'done', events: 9, declared_facts: 1,
    tokens: 920, started_at: at, ended_at: at, session_id: 'session_entry', model: 'example-model', purpose: '' },
  { id: 'r_evidence', intent_id: 'in_evidence', node: 'local', status: 'canceled', events: 4, declared_facts: 0,
    tokens: 310, started_at: at, ended_at: at, session_id: 'session_evidence', purpose: '',
    cancel_cause: 'operator' },
  { id: 'r_verify', intent_id: 'in_review', node: 'local', status: 'done', events: 3, declared_facts: 0,
    tokens: 260, started_at: at, ended_at: at, session_id: 'session_verify', purpose: 'verifier',
    warnings: ['示例告警：交接缺少验收条目'] },
];

// Facts a direction declared, as GET /api/intents/{id} returns them. Three
// statuses on purpose (verified / unverified / withdrawn) so the detail's
// counters and row labels are visible in one screenshot. Offline sample data.
const fixtureIntentFacts = {
  in_access: [
    { id: 'f_acc_1', intent_id: 'in_access', kind: 'vuln', title: '示例：账户越权读取他人订单',
      summary: '只读示例，不含真实数据。', confidence: 'observed', status: 'verified',
      severity: 'high', proof: 'dynamic', claim_version: 3, created_at: at,
      evidence_ids: ['e_acc_1', 'e_acc_2'], refs: [], tags: [] },
    { id: 'f_acc_2', intent_id: 'in_access', kind: 'host', title: '示例：边界主机响应带审计头',
      summary: '只读示例。', confidence: 'inferred', status: 'unverified', proof: 'dynamic',
      claim_version: 1, created_at: at, evidence_ids: ['e_acc_3'], refs: [], tags: [] },
    { id: 'f_acc_3', intent_id: 'in_access', kind: 'cred', title: '示例：只读账户凭据（已撤回）',
      summary: '凭据已由写入方撤回。', confidence: 'observed', status: 'withdrawn',
      claim_version: 1, created_at: at, evidence_ids: [], refs: [], tags: [] },
  ],
  // A pending direction that cites a premise declared by another direction: the
  // detail says the premise is missing from this response instead of assuming it
  // passed. That is the shape the ledger really has for a blocked direction.
  in_review: [],
  in_review_rejected: [],
  in_review_unfinished: [],
};

// Reports and their precheck. The precheck deliberately carries all three lists:
// facts no delivered report cites, one report whose rows cannot be read, and one
// that failed its own checks — the console must name the last two, not fold them
// into a zero. Offline sample data.
const fixtureReports = [
  { id: 'rep_fixture_1', status: 'ready', created_at: at, finished_at: at,
    source_digest: 'sha256:fixture', findings: 3, run_id: 'r_report',
    checks: [
      { rule: 'fact_exists', ok: true, detail: '三条结论都能在账本里找到' },
      { rule: 'evidence_present', ok: true, detail: '每条结论都有证据行' },
    ] },
  { id: 'rep_fixture_2', status: 'stale', created_at: at, finished_at: at,
    source_digest: 'sha256:older', findings: 2,
    checks: [{ rule: 'ledger_snapshot', ok: false, detail: '账本在生成后发生变化' }] },
];
const fixtureReportFindings = [
  { fact: 'f_a', verdict: 'confirmed', title: '示例：网关 OPTIONS 绕过鉴权', severity: 'medium' },
  { fact: 'f_b', verdict: 'confirmed', title: '示例：网关白名单直通下游 actuator', severity: 'high' },
  { fact: 'f_d', verdict: 'candidate', title: '示例：管理端导出接口缺少角色校验', severity: 'high' },
];
const fixturePrecheck = {
  reportable: 5, unverified: 1, rejected: 0, withdrawn: 1, running: false,
  unreported: {
    total: 23, limit: 20,
    items: [
      { fact: 'f_c', title: '示例：登录验证码可绕过' },
      { fact: 'f_e', title: '示例：对象存储列举权限外露' },
      { fact: 'f_g', title: '示例：预发环境调试端口可从办公网访问' },
    ],
    note: '只按已通过校验（status=ready）报告的 findings.json 里的 fact id 算差：命中即算已入，不判断该报告是否已过期。',
    unreadable: ['rep_old_1'],
    uncounted: ['rep_old_2'],
  },
};

// A pending inbox for the --pending shot: two handoffs, one authorization ask,
// one run warning, and tool approval unreadable. `countable` is false because
// one category is unknown, so the badge must show a lower bound instead of an
// exact number. Offline sample data only; the ids are synthetic.
const fixtureInboxPending = {
  // Sample B shape from the ledger: the control plane still says pending while
  // the body the commander wrote opens with a delivered-report marker.
  authorization: [{ id: 'req_fixture_1', status: 'pending', asset: 'portal.example.test（离线示例）',
    impact: '只影响示例环境的报告页',
    action: '【报告已交付（20:50）】报告已挂到项目报告页，等待复核',
    created_at: at, updated_at: at }],
  handoff: [
    { intent_id: 'in_evidence', title: '整理本轮原始证据', status: 'waiting',
      request: { id: 'ow_fixture_1', request: '需要你确认证据目录的保留期' } },
    { intent_id: 'in_entry', title: '确认示例环境入口', status: 'waiting',
      request: { id: 'ow_fixture_2', request: '入口清单里有两个地址无法连通，是否跳过' } },
  ],
  acceptance: [{ goal_id: 'g_access', statement: '核对边界外主机的访问是否受限', receipt_changed: ['f_2'] }],
  warnings: [{ run: 'r_verify', intent_id: 'in_review', warnings: ['示例告警：交接缺少验收条目'] }],
  guardrail: { state: 'unavailable', reason: '没有可用会话' },
  // Closed requests, read-only and outside the todo counts. Both carry the two
  // states that disagree, so the access page has to show both sides.
  recent: [
    { id: 'req_fixture_old_1', status: 'dismissed', resolution: 'work_resumed',
      resolved_at: at, created_at: at,
      action: '把测试范围扩到 10.0.0.0/24（示例）', asset: '10.0.0.0/24（示例）' },
    { id: 'req_fixture_old_2', status: 'dismissed', resolution: 'work_resumed',
      resolved_at: at, created_at: at,
      action: '对示例网关做并发扫描', asset: 'gateway.example.test（示例）' },
  ],
  total: 5,
  summary: {
    countable: false,
    total: 5,
    categories: [
      { kind: 'authorization', state: 'ok', count: 1, items: [{ kind: 'authorization', id: 'req_fixture_1' }] },
      { kind: 'handoff', state: 'ok', count: 2, items: [
        { kind: 'handoff', id: 'in_evidence', title: '整理本轮原始证据' },
        { kind: 'handoff', id: 'in_entry', title: '确认示例环境入口' },
      ] },
      { kind: 'acceptance', state: 'ok', count: 1, items: [{ kind: 'acceptance', id: 'g_access' }] },
      { kind: 'warning', state: 'ok', count: 1, items: [{ kind: 'warning', id: 'r_verify' }] },
      { kind: 'guardrail', state: 'unavailable', reason: '没有可用会话', count: 0, items: [] },
    ],
  },
};

// The live pre-run inbox right now: two run warnings, no authorization ask, no
// handoff, tool approval unreadable. Same shape the console reads at 14:12.
const fixtureInboxReal = {
  authorization: [],
  handoff: [],
  acceptance: [],
  warnings: [
    { run: 'r_warn_1', intent_id: 'in_fixture_1', warnings: ['示例告警：交接缺少验收条目'] },
    { run: 'r_warn_2', intent_id: 'in_fixture_2', warnings: ['示例告警：交接未带验收标准'] },
  ],
  guardrail: { state: 'unavailable', reason: '没有可用会话' },
  recent: [],
  total: 2,
  summary: {
    countable: false,
    total: 2,
    categories: [
      { kind: 'authorization', state: 'none', count: 0, items: [] },
      { kind: 'handoff', state: 'none', count: 0, items: [] },
      { kind: 'acceptance', state: 'none', count: 0, items: [] },
      { kind: 'warning', state: 'ok', count: 2, items: [
        { kind: 'warning', id: 'r_warn_1' },
        { kind: 'warning', id: 'r_warn_2' },
      ] },
      { kind: 'guardrail', state: 'unavailable', reason: '没有可用会话', count: 0, items: [] },
    ],
  },
};
// A participant whose title is a whole direction name is what breaks the meta
// row: the sender label must truncate instead of pushing the delivery badge off
// the row.
const longTitle = '113.219.171.189:9012 定案补账：admin/123456 管理员会话与未授权海康/GIS/pprof（只读）';
const fixtureMessages = [{
  id: 'm_long_1', seq: 41, project_id: 'fixture',
  from_participant_id: 'pt_long', to_participant_id: 'commander',
  kind: 'say',
  text: '[in_pxy4visls3 收口] 事实质量修正已完成：f_fqqsetvcog 已对齐到 pprof 事实。',
  status: 'accepted', delivery_path: 'prompt', created_at: at,
}];

const fixtureFactOf = (id, title, severity, createdAt = at) => ({
  id, intent_id: '', kind: 'vuln', title,
  summary: '示例：离线合成数据，不含真实项目。',
  confidence: 'observed', status: 'verified', severity,
  proof: 'dynamic', claim_version: 1, created_at: createdAt, evidence_ids: [], refs: [], tags: [],
});
const fixtureFact = fixtureFactOf('f_a', '示例：网关 OPTIONS 绕过鉴权', 'medium');
// The graph page carries a time axis: synthetic rows are spread over one working
// hour so the scrubber has somewhere to travel and the reveal is visible in a
// still capture. Every row is offline sample data.
const timedFact = (id, title, severity, minute) =>
  fixtureFactOf(id, title, severity, `2026-11-16T${String(9 + Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}:00+08:00`);
const fixtureFacts = [
  timedFact('f_a', '示例：网关 OPTIONS 绕过鉴权', 'medium', 2),
  timedFact('f_b', '示例：网关白名单直通下游 actuator', 'high', 9),
  timedFact('f_c', '示例：登录验证码可绕过', 'low', 16),
  timedFact('f_d', '示例：管理端导出接口缺少角色校验', 'high', 24),
  timedFact('f_e', '示例：对象存储列举权限外露', 'medium', 31),
  timedFact('f_f', '示例：任务调度接口未做会话绑定', 'medium', 38),
  timedFact('f_g', '示例：预发环境调试端口可从办公网访问', 'low', 46),
  timedFact('f_h', '示例：审计日志仅保留七天', 'low', 53),
];
const fixtureGraph = {
  version: 1, generated_at: at,
  snapshot_version: { epoch: 'e_fixture', seq: 1 }, total: 14, has_more: false, next_cursor: '',
  facts: fixtureFacts, goals: [], intents: [
    { id: 'in_gateway', title: '核对网关路由与鉴权', status: 'done', attempts: 1,
      created_at: '2026-11-16T09:05:00+08:00', updated_at: '2026-11-16T09:20:00+08:00', node: 'local', notes: [] },
    { id: 'in_export', title: '验证管理端导出权限', status: 'done', attempts: 1,
      created_at: '2026-11-16T09:22:00+08:00', updated_at: '2026-11-16T09:40:00+08:00', node: 'local', notes: [] },
  ],
  edges: [
    { id: 'e_b_a', source: 'f:f_b', target: 'f:f_a', kind: 'reference' },
    { id: 'e_p1', source: 'f:f_a', target: 's:in_gateway', kind: 'premise' },
    { id: 'e_p2', source: 'f:f_b', target: 's:in_gateway', kind: 'premise' },
    { id: 'e_i1', source: 's:in_gateway', target: 'f:f_c', kind: 'intent' },
    { id: 'e_i2', source: 's:in_gateway', target: 'f:f_d', kind: 'intent' },
    { id: 'e_p3', source: 'f:f_d', target: 's:in_export', kind: 'premise' },
    { id: 'e_i3', source: 's:in_export', target: 'f:f_e', kind: 'intent' },
    { id: 'e_i4', source: 's:in_export', target: 'f:f_f', kind: 'intent' },
  ],
  boundary: [],
  fact_meta: fixtureFacts.map(f => ({ id: f.id, needs_review: false, reviewed_claim_version: 1, evidence_count: 0, ref_count: 0 })),
  facts_total: fixtureFacts.length, intents_total: 2, goals_total: 0, edges_total: 8, boundary_total: 0,
};

const hubIntents = Array.from({ length: 60 }, (_, i) => ({
  id: `in_h${i}`, title: `示例意图 ${i + 1}`, status: 'done', attempts: 1,
  created_at: `2026-11-16T09:${String(i).padStart(2, '0')}:00+08:00`,
  updated_at: `2026-11-16T09:${String(i).padStart(2, '0')}:30+08:00`, node: 'local', notes: [],
}));
const hubFacts = hubIntents.flatMap((it, i) => [0, 1, 2, 3].map((j) => fixtureFactOf(
  `f_h${i}_${j}`, `示例产出 ${i + 1}-${j + 1}`, 'low',
  `2026-11-16T09:${String(i).padStart(2, '0')}:${String(10 + j * 5).padStart(2, '0')}+08:00`,
)));
hubFacts.forEach((f, i) => { f.intent_id = hubIntents[Math.floor(i / 4)].id; });
const fixtureHubGraph = {
  ...fixtureGraph,
  facts: hubFacts,
  intents: hubIntents,
  edges: [{ id: 'e_hub', source: 'f:f_h0_0', target: 'f:f_h1_0', kind: 'reference' }],
  fact_meta: hubFacts.map((f) => ({ id: f.id, needs_review: false, reviewed_claim_version: 1, evidence_count: 0, ref_count: 0 })),
  total: hubFacts.length + hubIntents.length + 1,
  facts_total: hubFacts.length, intents_total: hubIntents.length, goals_total: 0, edges_total: 1, boundary_total: 0,
};

// Tool page fixtures: twelve rows so the visible cap is exercised, with rates
// and durations spanning "good", "flaky", "slow" and "no result yet". All offline.
const fixtureToolRanks = [
  { tool: 'bash', calls: 412, runs: 38, completed: 400, failures: 12, duration_ms: 96_000, last_at: '2026-11-16T09:58:00+08:00',
    error_kinds: [{ label: 'exit code 1', count: 7 }, { label: 'no such file or directory', count: 3 }, { label: 'timeout', count: 2 }] },
  { tool: 'local:curl', local: true, calls: 166, runs: 24, completed: 160, failures: 9, duration_ms: 240_000, last_at: '2026-11-16T09:55:00+08:00',
    error_kinds: [{ label: 'unsupported flag --tlsv1.2', count: 5 }, { label: 'exit code 23', count: 4 }] },
  { tool: 'read_file', calls: 121, runs: 30, completed: 121, failures: 0, duration_ms: 8_000, last_at: '2026-11-16T09:57:00+08:00' },
  { tool: 'gogo', calls: 74, runs: 12, completed: 70, failures: 26, duration_ms: 1_120_000, last_at: '2026-11-16T09:40:00+08:00' },
  { tool: 'local:nuclei', local: true, calls: 58, runs: 9, completed: 58, failures: 2, duration_ms: 2_400_000, last_at: '2026-11-16T09:31:00+08:00' },
  { tool: 'write_file', calls: 44, runs: 18, completed: 44, failures: 0, duration_ms: 3_000, last_at: '2026-11-16T09:52:00+08:00' },
  { tool: 'rg', calls: 39, runs: 15, completed: 39, failures: 1, duration_ms: 6_500, last_at: '2026-11-16T09:47:00+08:00' },
  { tool: 'python', calls: 21, runs: 7, completed: 20, failures: 4, duration_ms: 88_000, last_at: '2026-11-16T09:22:00+08:00' },
  { tool: 'tokenize', calls: 8, runs: 3, completed: 0, failures: 0, duration_ms: 0, last_at: '2026-11-16T09:12:00+08:00' },
  { tool: 'local:spray', local: true, calls: 6, runs: 2, completed: 6, failures: 3, duration_ms: 30_000, last_at: '2026-11-16T09:05:00+08:00' },
  { tool: 'katana', calls: 3, runs: 1, completed: 3, failures: 0, duration_ms: 210_000, last_at: '2026-11-16T08:58:00+08:00' },
  { tool: 'local:neutron', local: true, calls: 1, runs: 1, completed: 1, failures: 1, duration_ms: 2_000, last_at: '2026-11-16T08:51:00+08:00' },
];
const fixtureArsenal = [
  { name: 'nuclei', version: 'v3.3.5', size: 41_000_000, source: 'download', managed_by: 'arsenal', dirs: ['C:/example/arsenal/bin/nuclei.exe'] },
  { name: 'gogo', version: 'v2.11.0', size: 18_400_000, source: 'bundle', managed_by: 'arsenal', dirs: ['C:/example/arsenal/bin/gogo.exe'] },
  { name: 'katana', version: 'v1.1.0', size: 22_000_000, source: 'download', managed_by: 'arsenal', dirs: ['C:/example/arsenal/bin/katana.exe'] },
];
const fixtureToolRoots = [
  { path: 'C:/example/arsenal', bin: 'C:/example/arsenal/bin', source: 'instance', exists: true, resolved: true, path_injected: true, note: '实例目录；当前宿主已从 PATH 解析到这里的工具。' },
  { path: 'C:/Users/example/.arsenal', bin: 'C:/Users/example/.arsenal/bin', source: 'user', exists: false, note: '用户目录；尚未创建，装到这里的工具不会出现在上面的清单里。' },
];
const fixtureRuntimeTools = {
  state: 'connected', agent_id: 'host-fixture', provider: 'example-provider', model: 'example-model',
  checked_at: at,
  commands: [
    { name: '!bash', description: '在宿主上执行命令（内嵌 POSIX 解释器）', usage: '!bash -lc "cmd"' },
    { name: '!read_file', description: '按行读取文本文件', usage: '!read_file path' },
    { name: '!write_file', description: '写入文件，自动创建父目录', usage: '!write_file path content' },
    { name: '/skill:http-probe', description: '按技能探测 HTTP 面', usage: '/skill:http-probe URL' },
    { name: '/compact', description: '压缩会话上下文' },
    { name: '!curl', description: '纯 Go 复刻的 curl', usage: '!curl -sS -o out.json URL' },
  ],
};

function api(url) {
  const route = url.pathname.split('/api/')[1];
  if (route && /^runs\/[^/]+\/events$/.test(route)) {
    return { events: [
      { at: recent, kind: 'tool_result', text: '读取示例账户的授权响应，记录边界字段。' },
      { at: recent, kind: 'tool_call', text: '调用只读接口核对权限。' },
      { at: recent, kind: 'turn_ended', text: '本轮结束。', stop_reason: 'end_turn' },
      { at: recent, kind: 'tool_result', text: '证据样本已登记到示例目录。' },
      { at: recent, kind: 'error', text: '一次只读请求超时，已重试。' },
      { at: recent, kind: 'tool_call', text: '继续核对下一个边界。' },
    ] };
  }
  // One fact detail (assertion version + the review bound to it + its evidence).
  // Every fact the fixture graph knows gets the same shape: the panel derives
  // "已复核 / 旧断言" from these fields, and a deep link used to hit the default
  // `{}` here — the panel then read `evidence` off an object that had none.
  if (route && /^facts\/[^/]+$/.test(route)) {
    const id = decodeURIComponent(route.slice('facts/'.length));
    const base = fixtureGraph.facts.find((f) => f.id === id) ?? fixtureFact;
    // Missing-evidence shape: the panel must name what it could not read.
    if (partialFactArg) return { fact: { ...base, claim_version: 1 }, version: 1 };
    return {
      fact: { ...base, claim_version: 1, evidence_ids: ['e_fixture_1'] },
      version: 1,
      evidence: [{
        id: 'e_fixture_1', run_id: 'r_access', intent_id: 'in_gateway',
        name: 'response-sample.txt', mime: 'text/plain', size: 168,
        uri: 'evidence/response-sample.txt', created_at: at,
      }],
      reviews: [{
        id: 'rv_fixture_1', fact_id: id, verdict: 'pass', claim_version: 1,
        claim_hash: 'sha256:fixture-claim', verifier_run: 'r_verify_fixture',
        notes: '示例复核：重跑并核对了响应样本', created_at: at,
      }],
    };
  }
  // The three project documents: current text + one revision of history. The
  // 约束 view is read-only here (the gallery may only GET), so a save is only
  // exercised by unit tests.
  if (route === 'documents') {
    return { documents: {
      scope: { revision: 7, updated_at: at, updated_by: 'operator',
        text: '授权资产（离线合成）：\n- portal.example.test\n- 10.0.0.0/24\n\n已知条件：只有只读账号，测试窗口在工作时间外。' },
      // The goal document has no saved-at field in the API: its revision is the
      // goal row's version, and the fixture mirrors that instead of inventing one.
      goal: { revision: 3, goal_id: 'g_evidence',
        text: '拿到至少一个可复现的控制点，并留下可复核的证据；拿不到就写清卡在哪一步。' },
      constraints: { revision: 5, updated_at: at, updated_by: 'operator',
        text: '约束（唯一硬线）：不要删除或修改真实数据——不改配置、不删文件、不锁账号、不改动业务数据。\n\n测试过程中产生的东西（上传的文件、新建的账号、写入的记录、临时对象等），能清理就在同一回合清理；清不掉的不用停，把「留了什么、在哪里、怎么清」写进事实或报告即可。' },
    } }
  }
  if (route && /^documents\/[^/]+\/history$/.test(route)) {
    const kind = route.split('/')[1]
    return { revisions: [
      { revision: 7, updated_at: at, updated_by: 'operator', text: 'current', diff: '@@ -3,3 +3,3 @@\n 已知条件：只有只读账号\n-测试窗口：不限\n+测试窗口：在工作时间外' },
      { revision: 6, updated_at: at, updated_by: 'operator', text: 'older', diff: '@@ -1,2 +1,2 @@\n-授权资产：待填\n+授权资产：portal.example.test' },
    ].map((row) => ({ ...row, revision: kind === 'goal' ? row.revision - 4 : row.revision })) }
  }  // Review rows for one fact. The 状态 page derives 「判定不通过」 vs 「没跑完」 from
  // whether a row exists, so the fixture carries both shapes: f_acc_2 was judged
  // (needs_evidence) and f_acc_3 has no row at all (an interrupted verifier run).
  if (route === 'reviews') {
    const fact = url.searchParams.get('fact') || ''
    if (fact === 'f_acc_2') {
      return { reviews: [{
        id: 'rv_fixture_no', fact_id: fact, verdict: 'needs_evidence', claim_version: 1,
        claim_hash: 'sha256:fixture-claim', verifier_run: 'r_verify_fixture',
        notes: '示例复核：重跑后仍无法复现，证据不足', created_at: at,
      }] };
    }
    return { reviews: [] };
  }
  // The report trace: the precheck answer, the report list, and the two files a
  // report delivers (findings.json is JSON, index.md is plain text).
  if (route === 'reports/precheck') return fixturePrecheck;
  if (route === 'reports') return { reports: fixtureReports };
  if (route && /^reports\/[^/]+\/file$/.test(route)) {
    const path = url.searchParams.get('path') || '';
    if (path === 'findings.json') return fixtureReportFindings;
    if (path === 'index.md') return { __raw: '# 应用安全评估示例（离线合成）\n\n本报告用于控制台截图，不含真实项目、凭据或运行材料。\n' };
    return { __raw: '' };
  }
  // One direction's own facts (GET /api/intents/{id}); the detail page reads
  // status and evidence from this ledger query, not from the case snapshot.
  if (route && /^intents\/[^/]+$/.test(route)) {
    const id = decodeURIComponent(route.slice('intents/'.length))
    const intent = fixtureIntents.find((row) => row.id === id)
    assert.ok(intent, 'unknown intent fixture: ' + id)
    const facts = fixtureIntentFacts[id] ?? []
    return { intent, facts, facts_total: facts.length, facts_has_more: false }
  }
  switch (route) {
    case 'fleet': {
      const run = { ...fixtureRuns[0], title: fixtureIntents[0].title };
      const active = {
        id: project.id, name: project.name, scope: project.scope,
        decider: { enabled: true, rounds: 3, activity: { phase: 'model', since: recent, detail: '正在核对项目状态' } },
        intents: { total: 4, pending: 1, claimed: 0, running: 1, done: 1, failed: 0, parked: 0 },
        facts: { total: 6, with_evidence: 4, verified: 4 }, evidence: 4,
        goals: { total: 2, met: 1 }, runs: { total: 1, running: 1, done: 0, failed: 0, canceled: 0 },
        running: [{ ...fixtureIntents[0], created_at: recent, run_id: run.id }],
        open: [{ ...fixtureIntents[3], created_at: recent }], recent_runs: [run], tokens: run.tokens,
      };
      const archive = { ...active, id: 'fixture-archive', name: '交接归档示例', decider: { enabled: true, paused: true, rounds: 2 },
        intents: { ...active.intents, pending: 0, running: 0 }, goals: { total: 1, met: 1 },
        running: [], open: [], recent_runs: [], tokens: 920 };
      return { generated_at: recent, projects: [active, archive],
        nodes: [{ id: 'local', running: 1, health: { online: true, checked_at: recent, latency_ms: 3 } }] };
    }
    case 'intents': return { intents: fixtureIntents, total: fixtureIntents.length, has_more: false };
    case 'runs': return { runs: fixtureRuns };
    case 'goals': return { goals: twoGoalsArg ? [
      { id: 'g_current', statement: '拿到至少一个可复现的控制点，并留下可复核的证据', met: false, created_at: at,
        start: '已有授权清单与只读账号', revision: 3 },
      { id: 'g_later', statement: '对已确认的候选漏洞给出最小复现', met: false, created_at: at,
        start: '从当前控制点继续', revision: 1 },
    ] : [
      // A met goal whose cited facts changed after the seal: the case the
      // inbox's 失效的验收回执 row links to (`?view=state&goal=g_access`).
      { id: 'g_access', statement: '核对边界外主机的访问是否受限', met: true, created_at: at,
        start: '已有授权清单与测试窗口', note: '示例：以只读方式确认了边界，未执行写操作',
        from: ['f_1', 'f_2'], completed_at: at,
        receipt_stale: true, receipt_changed: ['f_2'] },
      { id: 'g_evidence', statement: '每一条结论都有可复核证据', met: true, created_at: at },
    ] };
    case 'access': return { access: [] };
    case 'usage/breakdown': return {
      commander: { total: 3200, runs: 4 }, executor: { total: 5100, runs: 9 },
      executor_total: { total: 5100 }, judge: { jev: { total: 400 }, llm: { total: 120 } },
      combined: { total: 8300 }, all: { total: 8820 },
    };
    case 'operator-inbox': if (realCountsArg) return fixtureInboxReal;
    // The access page's `recent` shot is about closed requests whose two states
    // disagree; that list only exists in the pending fixture. Make the page
    // self-contained instead of silently rendering an empty history.
    if (selectedPages.includes('access-recent')) return fixtureInboxPending;
    return pendingArg ? fixtureInboxPending : {
      authorization: [], handoff: [], acceptance: [], warnings: [], total: 0,
      summary: {
        countable: true, total: 0,
        categories: ['authorization', 'handoff', 'acceptance', 'warning', 'guardrail']
          .map(kind => ({ kind, state: 'none', count: 0, items: [] })),
      },
    };
    case 'projects': return { projects: [project, { ...project, id: 'fixture-archive', name: '交接归档示例',
      running: 0, intents: 8, facts: 16, evidence: 24,
      // One project pins reviews closed; the first one follows the instance default.
      reviews_disabled: true, reviews_disabled_effective: false }], max_runs: 3, round_timeout_seconds: 900,
      // `null` is the healthy answer (field present, nothing quarantined); a
      // missing field would be an older build and renders nothing.
      vuln_library_quarantine: quarantineArg ? {
        path: '/srv/example/data/vulns/vulns.json.corrupt-20261124-151805',
        reason: 'parse /srv/example/data/vulns/vulns.json: unexpected end of JSON input',
        quarantined_at: at,
      } : null };
    case 'settings': return { configured: true, model: 'example-model', api_key_set: true,
      provider:'openai', base_url:'https://gateway.example.test/v1', context_window:128000, path: '~/.cyber/cyber.yaml',
      // Instance default for reviews; per-project overrides are separate.
      reviews_disabled: false };
    case 'workers': return { workers, count: workers.length };
    case 'peers': return { mode: 'open', peer_policy: { mode: 'open', allow: [] } };
    case 'participants': return { participants: [
      ...workers.map(w => ({ id: w.participant_id, title: w.title, intent_id: w.intent_id })),
      { id: 'pt_long', title: longTitle, intent_id: 'in_access' },
    ] };
    case 'messages': return { messages: fixtureMessages };
    case 'graph': return hubArg ? fixtureHubGraph : fixtureGraph;
    case 'tools': return {
      leaderboard: fixtureToolRanks, runs_scanned: 42, arsenal: fixtureArsenal,
      arsenal_dirs: ['C:/example/arsenal', 'C:/Users/example/.arsenal'],
      tool_roots: fixtureToolRoots,
      tool_roots_note: '根目录由实例自身的数据目录规则解析；目录存在不代表已进入当前宿主环境。',
    };
    case 'tools/runtime': return fixtureRuntimeTools;
    case 'changes': return { changes: [], has_more: false, next_cursor: '', high_watermark: { epoch: 'e_fixture', seq: 0 }, pending: 0 };
    case 'instructions': return { instructions: [], has_more: false, next_cursor: '' };
    case 'ioa/events': return { records: [], sessions: {} };
    case 'nodes': return { nodes: [], default: 'local' };
    case 'runtime-settings': return { auxiliary_concurrency: 1 };
    case 'board': {
      const board = { generated_at: at, case: 'fixture', project_paused: true,
      intents: { total: 4, running: 1, waiting: 1, done: 1, pending: 1, held: 0, failed: 0, parked: 0 },
      facts: { total: 6, verified: 4, by_kind: {}, by_origin: {}, with_evidence: 5, reviewable_unverified: 1, notes: 1 }, evidence: 4,
      runs: { total: 4, running: 1, done: 3, failed: 0, canceled: 1 }, goals: { total: 2, met: 1, open: [] },
      decider: { enabled: true, paused: true, paused_reason: '操作员暂停', rounds: 3, tokens_total: 3200,
        model: 'example-model', context_percent: 42, handoff_missing: 1 },
      playbooks: { confirmed: 2, candidates: 1, retired: 0, archived: 0 }, redispatch: { total: 1 },
      // The operator action contract (internal/api/project_actions.go). Manual
      // pause: 继续 is available, so the banner must not claim 已收口.
      project_actions: {
        state: { paused: true, kind: 'manual_pause', reason: '操作员暂停' },
        facts: { has_ledger: true, goals_total: 2, goals_met: 1, goals_open: 1, goals_active: 2,
          pending_directions: 1, unread_messages: 0 },
        actions: [
          { id: 'pause', enabled: false, reason_code: 'already_paused', reason: '项目已暂停' },
          { id: 'resume', enabled: true, reason_code: 'paused_manual', reason: '人工暂停：继续会重新派发待办意图' },
          { id: 'append_goal', enabled: true, reason_code: 'ok', reason: '追加一个新目标并继续（不改动已达成目标的完成收据）' },
        ],
        notes: [],
      },
      open_intents: [], running_intents: [],
      waiting_intents: [{ id: 'in_evidence', title: '整理本轮原始证据', status: 'waiting' }],
      recent_facts: [
        { id: 'f_1', title: '示例入口可访问', kind: 'observation', status: 'verified', evidence: 2, origin: 'executor' },
        { id: 'f_2', title: '示例账户缺少边界校验', kind: 'observation', status: 'unverified', evidence: 1, origin: 'executor' },
        { id: 'f_3', title: '示例响应带审计头', kind: 'observation', status: 'unverified', evidence: 0, origin: 'executor' },
      ],
      frontier: [
        // The board appends the reason to a blocked premise id (`blocked_on` in
        // internal/board/board.go), and the detail has to split id from reason.
        { id: 'in_review', title: '复核访问控制结论', ready: false, blocked_on: ['f_premise（已撤回）'],
          premise_changed: ['f_acc_1'], last_declared: 0 },
        { id: 's_1', title: '复核边界外主机访问', ready: true, last_declared: 1 },
        { id: 's_2', title: '补齐证据目录', ready: false, blocked_on: ['f_2'], last_declared: 0 },
      ],
      handoff_warnings: [] };
      if (realCountsArg) {
        board.runs = { ...board.runs, total: 341, failed: 9, running: 0 };
        board.intents = { ...board.intents, total: 57, pending: 7, held: 1, parked: 6, running: 0, waiting: 0 };
        board.goals = { ...board.goals, total: 2, met: 1 };
        board.decider = { ...board.decider, paused_reason: '控制面重启后自动恢复未完成：确认现场后继续' };
        board.project_actions = { ...board.project_actions,
          state: { ...board.project_actions.state, reason: '控制面重启后自动恢复未完成：确认现场后继续' },
          facts: { ...board.project_actions.facts, goals_total: 2, goals_met: 1, pending_directions: 7 } };
      }
      if (pauseArg) {
        // The structured cause the new codes carry. The reason sentence is the
        // one the control plane returns, so the console renders the same text a
        // live instance would: a restart-forced pause, or a pause whose cause
        // the manifest does not record. Nothing is inferred from prose.
        const reason = PAUSE_COPY[pauseArg];
        board.decider = { ...board.decider, paused: true, paused_reason: reason };
        board.project_actions = { ...board.project_actions,
          state: { paused: true, kind: pauseArg, reason },
          actions: board.project_actions.actions.map(action => action.id === 'resume'
            // nothing_to_continue is what makes 追加目标并继续 reachable: the
            // engagement is parked with nothing left to resume on its own.
            ? { ...action, enabled: true, reason_code: appendGoalArg ? 'nothing_to_continue' : action.reason_code }
            : action) };
      }
      return board
    }
    default: return { runs: [], nodes: [], projects: [], configured: true, model_configured: true };
  }
}

(async () => {
  let ws;
  const errors = [];
  try {
    const portFile = path.join(profileDir, 'DevToolsActivePort');
    let port = 0;
    for (let i = 0; i < 100 && !port; i++) {
      try {
        port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
      } catch (error) {
        if (error.code !== 'ENOENT' && error.code !== 'EBUSY') throw error;
      }
      if (!port) await pause(100);
    }
    assert.ok(port > 0, 'Chrome did not publish its DevTools port');
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find(p => p.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
    let seq = 0;
    const pending = new Map();
    const call = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++seq;
      const timer = setTimeout(() => { pending.delete(id); reject(Error('CDP timeout: ' + method)); }, 10000);
      pending.set(id, { resolve(value) { clearTimeout(timer); resolve(value); }, reject(error) { clearTimeout(timer); reject(error); } });
      ws.send(JSON.stringify({ id, method, params }));
    });
    ws.addEventListener('message', async event => {
      const msg = JSON.parse(event.data);
      if (msg.id) { const p = pending.get(msg.id); if (p) { pending.delete(msg.id); msg.error ? p.reject(Error(JSON.stringify(msg.error))) : p.resolve(msg.result); } return; }
      if (msg.method === 'Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      if (msg.method !== 'Fetch.requestPaused') return;
      const { requestId, request } = msg.params;
      const url = new URL(request.url);
      try {
        let body, type = 'application/json';
        if (url.pathname.startsWith('/static/')) {
          const file = path.resolve(web, url.pathname.slice('/static/'.length));
          assert.ok(file.startsWith(web + path.sep));
          body = fs.readFileSync(file);
          type = assetTypes[path.extname(file)] ?? 'application/octet-stream';
        } else if (url.pathname.includes('/api/')) {
          // One write is answered synthetically, and only when --append-goal is
          // set: the dialog's outcome line is rendered from the server's answer,
          // so there is no other honest way to capture it. Every other route
          // still refuses non-GET.
          if (appendGoalArg && url.pathname.endsWith('/api/project/append-goal')) {
            assert.equal(request.method, 'POST', 'append-goal capture needs a POST');
            body = Buffer.from(JSON.stringify({
              goal: { id: 'g_fixture_append', statement: '离线示例：在示例环境复现一次只读越权并留下原始响应',
                start: '上一目标已完成：示例门户与一个测试账号已确认', met: false, revision: 1,
                created_at: at, updated_at: at },
              created: true, resumed: appendResumedArg, resumed_from: appendResumedArg ? appendGoalArg : '',
              decider_woken: appendResumedArg, decider_paused: !appendResumedArg,
              paused_before: true, pause_kind_before: appendGoalArg,
              paused_after: !appendResumedArg, pause_kind_after: appendResumedArg ? 'running' : appendGoalArg,
              pause_reason: PAUSE_COPY[appendGoalArg], retryable: false,
              note: appendResumedArg
                // Quoted from internal/api/goal_continue.go (restart resume note) so
                // the capture shows the sentence the server really returns.
                ? '项目此前因控制面重启而暂停、自动恢复未完成，已按本次显式操作追加目标并恢复派发；旧目标的完成收据保持不变。'
                : '项目仍处于暂停：本次没有改动暂停状态，旧目标的完成收据保持不变。',
            }));
            await call('Fetch.fulfillRequest', { requestId, responseCode: 200,
              responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: body.toString('base64') });
            return;
          }
          assert.equal(request.method, 'GET', 'gallery may only read fixture APIs');
          const payload = api(url);
          if (payload && typeof payload.__raw === 'string') {
            body = Buffer.from(payload.__raw);
            type = 'text/plain; charset=utf-8';
          } else {
            body = Buffer.from(JSON.stringify(payload));
          }
        } else { body = fs.readFileSync(path.join(web, 'index.html')); type = 'text/html'; }
        await call('Fetch.fulfillRequest', { requestId, responseCode: 200,
          responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
      } catch (error) {
        errors.push(error.message);
        await call('Fetch.failRequest', { requestId, errorReason: 'Failed' });
      }
    });
    await call('Page.enable'); await call('Runtime.enable');
    await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    const evaluate = async expression => {
      const r = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (r.exceptionDetails) throw Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
      return r.result.value;
    };
    async function checkDisclosure(label, container, breakpoint) {
      const get = `(() => {
        const button = [...document.querySelectorAll('button[aria-controls][aria-expanded]')]
          .find(node => node.textContent.trim().startsWith(${JSON.stringify(label)}));
        const body = button && document.getElementById(button.getAttribute('aria-controls'));
        const area = document.querySelector(${JSON.stringify(container)});
        return {button, body, area};
      })()`;
      const current = () => evaluate(`(() => {
        const {button, body, area} = ${get};
        return {
          shown: Boolean(button?.getClientRects().length),
          expanded: button?.getAttribute('aria-expanded'),
          content: Boolean(body?.getClientRects().length),
          narrow: area?.clientWidth < ${breakpoint},
        };
      })()`);
      const initial = await current();
      assert.deepEqual(initial, {shown: initial.narrow, expanded: 'false', content: !initial.narrow, narrow: initial.narrow}, label);
      if (!initial.narrow) return;
      await evaluate(`${get}.button.click()`);
      assert.deepEqual(await current(), {shown: true, expanded: 'true', content: true, narrow: true}, label + ' opens');
      await evaluate(`${get}.button.click()`);
      assert.deepEqual(await current(), initial, label + ' closes');
    }
    const captured = [];
    for (const id of selectedPages) {
      const page = PAGES[id];
      const width = viewportArg ? viewportWidth : 1440;
      const height = viewportArg ? viewportHeight : page.height;
      await call('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
      await call('Page.navigate', { url: 'http://saker.test' + page.route });
      let ready = false;
      for (let i = 0; i < 100 && !ready; i++) {
        ready = await evaluate('document.body.innerText.includes(' + JSON.stringify(page.expected) + ')');
        if (!ready) await pause(75);
      }
      assert.ok(ready, page.route + ' did not render');
      await pause(250);
      // Optional interaction before the shot (open a report preview / a details
      // block). Asserted, so a renamed control fails the run instead of quietly
      // capturing the collapsed page.
      if (clickArg) {
        const clicked = await evaluate(`(() => {
          const el = document.querySelector(${JSON.stringify(clickArg)});
          if (!el) return false;
          el.click();
          return true;
        })()`);
        assert.ok(clicked, 'click target not found: ' + clickArg);
        await pause(300);
      }
      // Optional interaction: append a goal and capture the server's answer.
      // Asserted at every step, so a renamed control fails the run instead of
      // capturing a dialog that never opened or never submitted.
      if (appendGoalArg && id === 'graph-map') {
        const more = await evaluate(`(() => {
          const b = document.querySelector('button[aria-label="更多项目操作"]');
          if (!b) return false;
          b.click();
          return true;
        })()`);
        assert.ok(more, '更多项目操作 not found');
        await pause(300);
        const opened = await evaluate(`(() => {
          const b = [...document.querySelectorAll('button')]
            .find(x => (x.textContent || '').includes('追加目标并继续') && !x.closest('[data-testid="append-goal-dialog"]'));
          if (!b) return false;
          b.click();
          return true;
        })()`);
        assert.ok(opened, 'append-goal entry not found (project must be parked with nothing to continue)');
        await pause(400);
        const filled = await evaluate(`(() => {
          const dialog = document.querySelector('[data-testid="append-goal-dialog"]');
          const areas = dialog ? dialog.querySelectorAll('textarea') : [];
          if (!areas.length) return false;
          const el = areas[areas.length - 1];
          const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
          set.call(el, '离线示例：复读一次只读越权并留下原始响应与时间');
          el.dispatchEvent(new Event('input', { bubbles: true }));
          return true;
        })()`);
        assert.ok(filled, 'append-goal statement field not found');
        await pause(200);
        const submitted = await evaluate(`(() => {
          const dialog = document.querySelector('[data-testid="append-goal-dialog"]');
          const b = dialog && [...dialog.querySelectorAll('button')].find(x => (x.textContent || '').trim() === '追加目标并继续');
          if (!b) return false;
          b.click();
          return true;
        })()`);
        assert.ok(submitted, 'append-goal submit not found');
        let outcome = false;
        for (let i = 0; i < 60 && !outcome; i++) {
          outcome = await evaluate(`Boolean(document.querySelector('[data-testid="append-goal-success"]'))`);
          if (!outcome) await pause(100);
        }
        assert.ok(outcome, 'append-goal outcome did not render');
        await pause(250);
        const lineNeedle = appendResumedArg ? '已恢复派发' : '项目仍在暂停';
        const line = await evaluate(`(() => {
          const row = document.querySelector('[data-testid="append-goal-success"]');
          const span = row && [...row.querySelectorAll('span')].find(s => (s.textContent || '').includes(${JSON.stringify(lineNeedle)}));
          const r = span && span.getBoundingClientRect();
          return { text: span ? span.textContent.trim() : null, w: r ? Math.round(r.width) : null,
            h: r ? Math.round(r.height) : null, scrollW: span ? span.scrollWidth : null,
            clientW: span ? span.clientWidth : null };
        })()`);
        console.log('APPEND ' + JSON.stringify(line));
        assert.ok(line.text && line.text.includes(PAUSE_LABEL[appendGoalArg]),
          'outcome line does not name the pause kind: ' + line.text);
      }
      // Debugging aid: print one element's text after render (no screenshot).
      if (probeArg) {
        const text = await evaluate(`document.querySelector(${JSON.stringify(probeArg)})?.innerText ?? ''`);
        console.log('PROBE ' + JSON.stringify(text));
      }
      if (probeJS) console.log('EVAL ' + JSON.stringify(await evaluate(probeJS)));
      if (viewsArg) {
        await evaluate(`document.querySelector('[aria-label="视图"]')?.click()`);
        await pause(200);
      }
      if (id === 'messages') {
        await checkDisclosure('参与者与积压', 'main > .\\@container', 768);
        assert.ok(await evaluate(`document.querySelector('[aria-label="对话记录"]').clientHeight >= 144`), 'conversation has a readable log');
      }
      if (id === 'runs') {
        await checkDisclosure('运行列表', 'main > .\\@container', 768);
        assert.ok(await evaluate(`(() => {
          const viewport = document.querySelector('.saker-run-stream [data-radix-scroll-area-viewport]');
          return viewport && viewport.clientWidth > 0 && viewport.scrollWidth <= viewport.clientWidth;
        })()`), 'run events fit their scroll pane');
        // Event prose used to follow an inline strip of raw metadata at the same weight.
        assert.ok(await evaluate(`(() => {
          const row = document.querySelector('[aria-label="运行事件组"] article button');
          const summary = row.querySelector('.line-clamp-2');
          const rawKind = row.querySelector('.font-mono');
          return summary.getBoundingClientRect().top < rawKind.getBoundingClientRect().top
            && parseFloat(getComputedStyle(summary).fontSize) > parseFloat(getComputedStyle(rawKind).fontSize);
        })()`), 'event summary precedes and outweighs the raw event kind');
      }
      if (id === 'state') await checkDisclosure('意图列表', 'main > .\\@container', 768);
      if (id === 'overview') {
        // Three metrics in two columns left a blank tile and pushed down the inbox.
        assert.equal(await evaluate(`getComputedStyle(document.querySelector('[aria-label="关键指标"] .saker-metric-grid')).gridTemplateColumns.split(' ').length`), 3);
      }
      if (id === 'graph-map') {
        assert.ok(await evaluate(`(() => {
          const search = document.querySelector('.saker-graph-filter').getBoundingClientRect();
          const tools = document.querySelector('.saker-graph-toolbar').getBoundingClientRect();
          return search.right <= tools.left || search.top >= tools.bottom;
        })()`), 'graph search and view controls do not overlap');
        if (width < 704) {
          // Collapsing the two bottom overlays gives the small map back its height.
          assert.deepEqual(await evaluate(`(() => {
            const menu = document.querySelector('button[aria-label="更多图谱操作"]');
            const toggle = document.querySelector('.saker-graph-minimap button');
            const commander = document.querySelector('[title="展开指挥官状态"]');
            return [menu.getClientRects().length, toggle.getAttribute('aria-expanded'), commander?.getAttribute('aria-expanded'), !!document.querySelector('[data-testid="graph-minimap"]')];
          })()`), [1, 'false', 'false', false]);
          await evaluate(`document.querySelector('.saker-graph-minimap button').click()`);
          assert.ok(await evaluate(`Boolean(document.querySelector('[data-testid="graph-minimap"]'))`), 'minimap can still be opened');
          await evaluate(`document.querySelector('.saker-graph-minimap button[aria-expanded]').click()`);
          assert.ok(await evaluate(`!document.querySelector('[data-testid="graph-minimap"]')`), 'minimap can be closed again');
          await evaluate(`document.querySelector('[aria-label="更多图谱操作"]').dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true}))`);
          assert.deepEqual(await evaluate(`Array.from(document.querySelectorAll('[role="menuitem"]'), item => item.textContent.trim())`), ['导出全图', '刷新']);
          await evaluate(`document.querySelector('[role="menuitem"]').dispatchEvent(new KeyboardEvent('keydown', {key: 'Escape', bubbles: true}))`);
        }
      }
      if (searchArg && id === 'graph') {
        // React controls the input: set through the native setter, then dispatch
        // the same input event a keystroke would, or the state never updates.
        const typed = await evaluate(`(() => {
          const el = document.querySelector('input[aria-label="搜索节点"]');
          if (!el) return false;
          const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          set.call(el, ${JSON.stringify(searchArg)});
          el.dispatchEvent(new Event('input', { bubbles: true }));
          return true;
        })()`);
        assert.ok(typed, 'graph search box not found');
        await pause(600);
      }
      if (chainArg === 'path' && id === 'graph') {
        // The single-chain view: the node is already selected by the route hash,
        // so the button exists in the strip (wide) or the node panel (narrow).
        const clicked = await evaluate(`(() => {
          const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('只看主路径'));
          if (!btn) return false;
          btn.click();
          return true;
        })()`);
        assert.ok(clicked, 'chain path button not found');
        await pause(700);
      }
      if (timelineArg && id === 'graph') {
        // The axis is closed by default and has no footer while closed; open it
        // from the map toolbar switch, the same click the operator makes.
        await evaluate(`(() => {
          if (document.querySelector('input[aria-label="按入账顺序揭示节点"]')) return true;
          // 时间轴开关在「视图」面板的「显示」段：先开面板，再点那一行。
          const view = document.querySelector('button[aria-label="视图"]');
          if (!view) return false;
          view.click();
          return true;
        })()`);
        await pause(250);
        await evaluate(`(() => {
          const open = [...document.querySelectorAll('button')]
            .find((b) => (b.textContent || '').includes('时间轴'));
          if (!open) return false;
          open.click();
          return true;
        })()`);
        await pause(300);
        // Scrub the axis: native setter + input event, the same route a drag takes.
        const scrubbed = await evaluate(`(() => {
          const el = document.querySelector('input[aria-label="按入账顺序揭示节点"]');
          if (!el) return false;
          const last = Number(el.max);
          const next = Math.round((last * ${Number(timelineArg)}) / 100);
          const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          set.call(el, String(next));
          el.dispatchEvent(new Event('input', { bubbles: true }));
          return true;
        })()`);
        assert.ok(scrubbed, 'timeline slider not found');
        await pause(500);
      }
      if (expandArg) {
        await evaluate(`(() => {
          for (const el of document.querySelectorAll('details')) el.open = true;
          return true;
        })()`);
        await pause(200);
      }
      // The console themes by class (.dark), not by media query: apply it to the
      // document element after the page renders.
      if (themeArg) {
        await evaluate(`document.documentElement.classList.${themeArg === 'dark' ? 'add' : 'remove'}('dark')`);
        await pause(200);
      }
      if (id === 'graph-map') {
        // Sample outside the lanes, where the canvas should have the same base
        // as the rest of the page, including after a light theme switch.
        assert.ok(await evaluate(`(() => {
          const canvas = document.querySelector('[data-testid="graph-canvas"]');
          const ctx = canvas.getContext('2d');
          const actual = ctx.getImageData(1, 2, 1, 1).data;
          const sample = document.createElement('canvas').getContext('2d');
          sample.fillStyle = getComputedStyle(document.documentElement).getPropertyValue('--background').trim();
          sample.fillRect(0, 0, 1, 1);
          return actual.every((channel, i) => channel === sample.getImageData(0, 0, 1, 1).data[i]);
        })()`), 'graph canvas base follows the current theme');
      }
      if (id === 'workers') assert.ok(await evaluate('!document.body.innerText.includes("造人") && !document.body.innerText.includes("造一个人")'));
      const overflow = await evaluate(`(() => {
        const w = innerWidth;
        // An element that sticks out horizontally is only a defect when no
        // ancestor can scroll it back into view. A wide table inside an
        // overflow:auto row is reachable; the same table inside an
        // overflow:hidden row is clipped and its columns are lost.
        const reachableX = (el) => {
          let p = el.parentElement;
          while (p) {
            const cs = getComputedStyle(p);
            const canX = cs.overflowX === 'auto' || cs.overflowX === 'scroll';
            if (canX && p.scrollWidth > p.clientWidth + 1) return true;
            p = p.parentElement;
          }
          return false;
        };
        const bad = [...document.querySelectorAll('body *')].filter(el => {
          const r = el.getBoundingClientRect();
          return r.width > 0 && (r.right > w + 1 || r.left < -1) && !reachableX(el);
        }).map(el => {
          const r = el.getBoundingClientRect();
          return { tag: el.tagName, cls: String(el.className).slice(0, 70), left: Math.round(r.left), right: Math.round(r.right) };
        });
        return { scrollWidth: document.documentElement.scrollWidth, innerWidth: w, bad };
      })()`);
      if (overflow.bad.length) console.log('BAD ' + id + ' ' + JSON.stringify(overflow.bad));
      if (!allowOverflow) {
        assert.ok(overflow.scrollWidth <= overflow.innerWidth, 'horizontal overflow on ' + id + ': ' + JSON.stringify(overflow.bad.slice(0, 5)));
        // scrollWidth only catches elements that push the page wider. An element
        // cut off by an `overflow:hidden` ancestor leaves the page exactly as
        // wide as the viewport, so the labels inside it are simply unreadable —
        // that was the 640px token panel. Check the rects themselves; a page
        // that is known to be broken can still be captured with --allow-overflow.
        assert.deepEqual(
          overflow.bad.slice(0, 5),
          [],
          'element outside the viewport on ' + id + ' at ' + width + 'px: ' + JSON.stringify(overflow.bad.slice(0, 5)),
        );
      }
      // The header token panel is left-anchored below `lg` (1024) and right-
      // anchored above it. Both anchors must end up inside the viewport, so the
      // assertion is the same condition on every width — checking only <=700
      // left the whole 701–1023 left-anchored range untested, and the switch
      // point itself (1024) is the case most likely to be missed.
      //
      // Anchor is read from geometry, not from `getComputedStyle().left`: for an
      // absolutely positioned box Chrome reports the used value (`-170.4px`),
      // never `auto`, so a style check cannot tell the two anchors apart.
      const tokenPanel = await evaluate(`(() => {
        const host = document.querySelector('[data-testid="project-tokens"]');
        if (!host) return null;
        const panel = [...host.querySelectorAll('span')].find(el => getComputedStyle(el).position === 'absolute');
        if (!panel) return null;
        const h = host.getBoundingClientRect();
        const r = panel.getBoundingClientRect();
        return {
          left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width),
          capsuleLeft: Math.round(h.left), capsuleRight: Math.round(h.right),
          lg: matchMedia('(min-width: 1024px)').matches,
        };
      })()`);
      if (tokenPanel) {
        assert.ok(
          tokenPanel.left >= 0 && tokenPanel.right <= width + 1,
          'token panel clipped on ' + id + ' at ' + width + 'px: ' + JSON.stringify(tokenPanel),
        );
        // Anchor follows `lg`, not the pixel count: at 1024 a scrollbar can leave
        // the media query on the narrow side (innerWidth includes the scrollbar),
        // so the measured anchor is compared with the media query, not with width.
        const anchoredRight = Math.abs(tokenPanel.right - tokenPanel.capsuleRight) <= 1;
        const anchoredLeft = Math.abs(tokenPanel.left - tokenPanel.capsuleLeft) <= 1;
        assert.equal(
          tokenPanel.lg ? anchoredRight : anchoredLeft,
          true,
          'token panel anchored to the wrong side at ' + width + 'px (lg=' + tokenPanel.lg + '): ' + JSON.stringify(tokenPanel),
        );
      }
      assert.deepEqual(errors, []);
      const suffix = (viewportArg || themeArg || pagesArg) ? '-' + width + 'x' + height + (themeArg ? '-' + themeArg : '') : '';
      const name = page.name.replace(/\.png$/, suffix + '.png');
      const shot = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const png = Buffer.from(shot.data, 'base64');
      assert.ok(png.length > 15000);
      fs.writeFileSync(path.join(out, name), png);
      captured.push({ page: id, file: path.relative(root, path.join(out, name)), width, height, theme: themeArg || 'default', bytes: png.length });
    }
    console.log(JSON.stringify({ passed: true, screenshots: captured, synthetic: true, runtimeErrors: errors }));
  } finally {
    if (ws) ws.close();
    if (process.platform === 'win32') spawn('taskkill', ['/pid', String(chrome.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    else chrome.kill();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
