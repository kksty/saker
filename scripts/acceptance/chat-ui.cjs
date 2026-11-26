// Offline browser acceptance for the operator's 下发 entry points, against the
// current console surface:
//
//   - the 图谱 page's right dock ([data-graph-chat]) is only the *entry point*
//     now: it names 通信, offers 打开通信页 and closes;
//   - the unified composer ([data-directive-composer]) lives on the 通信 page,
//     where 时效 / 对象 select one of three effects (one-shot message to the
//     commander or to the running executors, steer, or a persistent
//     instruction) and the receipt says which one happened.
//
// Every HTTP response is mocked here from synthetic fixtures — no console, model,
// live project or uncommitted fixture file is touched (the old script read
// .build/ui-qa/messages-fixture, which only exists on a machine that captured
// it). The only writes are the mocked POST /p/fixture/api/directives.
//
// Run with node scripts/acceptance/chat-ui.cjs; SAKER_CHROME overrides Chrome.
// Screenshots and the DOM dump are written under .build/ui-qa/.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '../..');
const out = path.join(root, '.build/ui-qa');
const staticRoot = path.join(root, 'internal/web');
fs.mkdirSync(out, { recursive: true });

const graphURL = 'http://saker.test/p/fixture/?view=graph';
const messagesURL = 'http://saker.test/p/fixture/?view=messages';
const profile = fs.mkdtempSync(path.join(out, 'chrome-'));
const chrome = spawn(
  process.env.SAKER_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
  [
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    '--remote-debugging-port=0',
    '--remote-debugging-address=127.0.0.1',
    '--user-data-dir=' + profile,
    'about:blank',
  ],
  { windowsHide: true, stdio: 'ignore' },
);
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** Kill Chrome together with its helper processes (Windows leaks crashpad kids). */
function stopChrome(proc) {
  if (!proc) return;
  try {
    if (process.platform === 'win32') {
      spawn('taskkill', ['/pid', String(proc.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    } else {
      proc.kill();
    }
  } catch {
    proc.kill();
  }
}

function pngSize(buffer) {
  if (buffer.length < 24 || buffer.toString('ascii', 1, 4) !== 'PNG') return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

// ---- synthetic fixtures ----------------------------------------------------
// Small valid shapes, not captured data: one running direction, one settled
// direction, one open goal and an empty graph/feed. The pages only read these
// through optional chains or `?? []`, so the shell renders the real components
// without inventing project content.
const SNAPSHOT = { epoch: 'e1', seq: 1 };
const project = {
  id: 'fixture',
  name: '验收项目',
  scope: '仅限本地合成目标',
  created: '2026-11-18T00:00:00Z',
  intents: 2,
  facts: 0,
  evidence: 0,
  running: 1,
  max_runs_effective: 4,
};
const participants = [
  { id: 'commander', kind: 'commander', title: '指挥官', status: 'running', run_id: 'r_cmd', contactable: true },
  { id: 'pt_in_run_1', kind: 'intent', intent_id: 'in_run_1', title: '侦察意图', status: 'running', run_id: 'r_run_1', contactable: true },
  { id: 'pt_in_done_1', kind: 'intent', intent_id: 'in_done_1', title: '已收尾意图', status: 'done', contactable: false },
];
const intents = [
  { id: 'in_run_1', title: '侦察意图', description: '只读侦察合成目标', status: 'running', run_id: 'r_run_1' },
  { id: 'in_done_1', title: '已收尾意图', description: '已经收尾', status: 'done' },
];
const board = {
  generated_at: '2026-11-18T00:00:00Z',
  intents: { total: 2, pending: 0, held: 0, claimed: 0, running: 1, waiting: 0, reviewing: 0, done: 1, failed: 0, parked: 0 },
  facts: { total: 0, with_evidence: 0, verified: 0, by_kind: {}, by_origin: {} },
  evidence: 0,
  runs: { total: 1, running: 1, done: 0, failed: 0, canceled: 0 },
  goals: {
    total: 1,
    met: 0,
    closed: 0,
    open: [{ id: 'g_1', statement: '完成通信下发入口验收', start: '确认下发入口的当前落点', met: false, created_at: '2026-11-18T00:00:00Z' }],
  },
  open_intents: [],
  waiting_intents: [],
  reviewing_intents: [],
  review_failed_intents: [],
  running_intents: [],
  recent_facts: [],
  done_intents: ['in_done_1'],
  decider: { enabled: false, model: 'fake-model', rounds: 0, tokens_total: 0, stalled: 0 },
  project_paused: false,
  usage: {
    total_tokens: 0,
    hour_tokens: 0,
    input_tokens: 0,
    output_tokens: 0,
    cached_input_tokens: 0,
    cache_write_tokens: 0,
    requests: 0,
    missing_usage: 0,
    unknown_usage_runs: 0,
  },
};
const emptyPage = { has_more: false, next_cursor: '', total: 0, snapshot_version: SNAPSHOT };
const instructions = [];
const routes = {
  '/api/projects': { projects: [project], max_runs: 4 },
  '/p/fixture/api/board': board,
  '/p/fixture/api/participants': { participants },
  '/p/fixture/api/intents': { ...emptyPage, total: intents.length, intents },
  '/p/fixture/api/messages': { messages: [] },
  '/p/fixture/api/communication': { queues: [], max_messages: 200, max_bytes: 65536, paused: false },
  '/p/fixture/api/runs': { runs: [] },
  '/p/fixture/api/changes': { changes: [], has_more: false, high_watermark: SNAPSHOT, pending: 0 },
  '/p/fixture/api/operator-requests': { requests: [] },
  '/p/fixture/api/graph': {
    ...emptyPage,
    facts: [],
    intents: [],
    goals: [],
    edges: [],
    boundary: [],
  },
};

// The unified directive endpoint: one answer per effect, exactly the shapes
// lib/directive.ts renders receipts from.
let directives = [];
let backpressure = false;
function directiveAnswer(body) {
  if (backpressure) {
    return {
      code: 429,
      value: { error: '收件队列已满：稍后重试同一内容', code: 'message_backpressure' },
    };
  }
  if (body.target === 'executors') {
    return {
      kind: 'broadcast',
      target: 'executors',
      persistent: false,
      created: true,
      total: 2,
      failed: 1,
      recipients: [
        { intent_id: 'in_run_1', created: true, message: { id: 'm_dir_broadcast_1' } },
        { intent_id: 'in_done_1', created: false, error: '已结束/停放的意图不会收到' },
      ],
    };
  }
  if (body.target === 'project') {
    const instruction = {
      id: 'ins_1',
      version: 1,
      state: 'active',
      text: body.text,
      created_by: 'operator',
      created_at: '2026-11-18T00:00:00Z',
      updated_at: '2026-11-18T00:00:00Z',
    };
    // The standing-set view reads the list endpoint; the created row has to be
    // there when the page switches to the 持久指令 tab after a successful write.
    instructions.push(instruction);
    return { kind: 'instruction', target: 'project', persistent: true, created: true, instruction };
  }
  if (body.target === 'intent') {
    if (body.steer) {
      return { kind: 'steer', target: 'intent', persistent: false, created: true, intent_id: body.intent_id, run_id: 'r_steer_1' };
    }
    return {
      kind: 'message',
      target: 'intent',
      persistent: false,
      created: true,
      intent_id: body.intent_id,
      delivery: 'queued',
      message: { id: 'm_dir_intent_1' },
    };
  }
  return {
    kind: 'message',
    target: 'commander',
    persistent: false,
    created: true,
    delivery: 'queued',
    message: { id: 'm_dir_cmd_1' },
  };
}

const CONTENT_TYPES = {
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};

function mockedResponse(url, request) {
  if (url.pathname.startsWith('/static/')) {
    const file = path.join(staticRoot, decodeURIComponent(url.pathname));
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
      return { code: 404, type: 'text/plain; charset=utf-8', body: Buffer.from('not found') };
    }
    return {
      code: 200,
      type: CONTENT_TYPES[path.extname(file)] || 'application/octet-stream',
      body: fs.readFileSync(file),
    };
  }
  if (url.pathname === '/p/fixture/api/directives' && request.method === 'POST') {
    let body = null;
    try {
      body = JSON.parse(request.postData || '{}');
    } catch {
      body = { parse_error: request.postData };
    }
    directives.push(body);
    const answer = directiveAnswer(body);
    return {
      code: answer.code || 200,
      type: 'application/json; charset=utf-8',
      body: Buffer.from(JSON.stringify(answer.value || answer)),
    };
  }
  if (url.pathname === '/p/fixture/api/instructions') {
    return {
      code: 200,
      type: 'application/json; charset=utf-8',
      body: Buffer.from(JSON.stringify({ ...emptyPage, total: instructions.length, instructions })),
    };
  }
  if (routes[url.pathname]) {
    return { code: 200, type: 'application/json; charset=utf-8', body: Buffer.from(JSON.stringify(routes[url.pathname])) };
  }
  if (url.pathname.includes('/api/')) {
    return { code: 200, type: 'application/json; charset=utf-8', body: Buffer.from('{}') };
  }
  return {
    code: 200,
    type: 'text/html; charset=utf-8',
    body: fs.readFileSync(path.join(staticRoot, 'static/index.html')),
  };
}

(async () => {
  const checks = [];
  const check = (name, passed, expected, actual) => {
    checks.push({ name, passed: Boolean(passed), expected, ...(actual === undefined ? {} : { actual }) });
    assert.ok(passed, name + ': ' + JSON.stringify(actual));
  };

  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('Chrome did not open a DevTools port');
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find((x) => x.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', reject, { once: true });
    });

    let seq = 0;
    const pending = new Map();
    const errors = [];
    const call = (method, params = {}) =>
      new Promise((resolve, reject) => {
        const id = ++seq;
        const timer = setTimeout(() => {
          pending.delete(id);
          reject(new Error('CDP timeout ' + method));
        }, 8000);
        pending.set(id, {
          resolve: (v) => {
            clearTimeout(timer);
            resolve(v);
          },
          reject: (e) => {
            clearTimeout(timer);
            reject(e);
          },
        });
        ws.send(JSON.stringify({ id, method, params }));
      });
    ws.addEventListener('message', async (evt) => {
      const msg = JSON.parse(evt.data);
      if (msg.id) {
        const p = pending.get(msg.id);
        if (p) {
          pending.delete(msg.id);
          msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
        }
        return;
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      }
      if (msg.method === 'Fetch.requestPaused') {
        const { requestId, request } = msg.params;
        let response;
        try {
          response = mockedResponse(new URL(request.url), request);
        } catch (e) {
          errors.push('mock failed for ' + request.url + ': ' + e.message);
          response = { code: 500, type: 'text/plain', body: Buffer.from('mock failed') };
        }
        try {
          await call('Fetch.fulfillRequest', {
            requestId,
            responseCode: response.code,
            responseHeaders: [{ name: 'Content-Type', value: response.type }],
            body: response.body.toString('base64'),
          });
        } catch (e) {
          errors.push(e.message);
        }
      }
    });

    await call('Page.enable');
    await call('Runtime.enable');
    await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });

    const evalJS = async (expression) => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) {
        throw new Error('page exception: ' + (result.exceptionDetails.exception?.description || result.exceptionDetails.text));
      }
      return result.result.value;
    };
    const waitJS = async (expression, label) => {
      for (let i = 0; i < 150; i++) {
        if (await evalJS(expression)) return;
        await pause(100);
      }
      throw new Error('Timed out waiting for ' + label + ' / ' + (await evalJS('document.body.innerText')).slice(0, 800));
    };
    const waitText = (text) => waitJS('document.body.innerText.includes(' + JSON.stringify(text) + ')', text);
    const rectOf = (expression) => evalJS('(() => { const el = ' + expression + '; if (!el) return null; el.scrollIntoView({block:"center"}); const r = el.getBoundingClientRect(); return {x:r.left+r.width/2, y:r.top+r.height/2, w:r.width, h:r.height, text:(el.textContent||"").trim()}; })()');
    const clickPoint = async (point) => {
      await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x: point.x, y: point.y });
      await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, x: point.x, y: point.y });
      await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, x: point.x, y: point.y });
      await pause(120);
    };
    const clickSelector = async (selector) => {
      const point = await rectOf('document.querySelector(' + JSON.stringify(selector) + ')');
      if (!point) throw new Error('Selector not found: ' + selector);
      await clickPoint(point);
    };
    const clickButtonIn = async (groupSelector, label) => {
      const point = await rectOf(
        '[...(document.querySelector(' + JSON.stringify(groupSelector) + ')?.querySelectorAll("button") ?? [])].find((b) => b.textContent.trim() === ' +
          JSON.stringify(label) +
          ')',
      );
      if (!point) throw new Error('Button not found in ' + groupSelector + ': ' + label);
      await clickPoint(point);
    };
    const typeInto = ({ selector, text }) =>
      evalJS(`(() => {
        const ta = document.querySelector(${JSON.stringify(selector)});
        if (!ta) return null;
        const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
        setter.call(ta, ${JSON.stringify(text)});
        ta.dispatchEvent(new Event('input', { bubbles: true }));
        return ta.value;
      })()`);
    const pressEnter = (selector) =>
      evalJS(`(() => {
        const ta = document.querySelector(${JSON.stringify(selector)});
        if (!ta) return false;
        ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true, cancelable: true }));
        return true;
      })()`);
    const shot = async (name) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      return { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer) };
    };

    const readDock = () =>
      evalJS(`(() => {
        const dock = document.querySelector('[data-graph-chat]');
        if (!dock) return null;
        const rect = dock.getBoundingClientRect();
        return {
          text: dock.innerText,
          width: Math.round(rect.width),
          right: Math.round(innerWidth - rect.right),
          top: Math.round(rect.top),
          hasClose: Boolean(dock.querySelector('button[aria-label="关闭通信入口"]')),
          openConversation: [...dock.querySelectorAll('button')].some((b) => b.textContent.trim() === '打开通信页'),
          hasTextarea: Boolean(dock.querySelector('textarea')),
          hasComposer: Boolean(dock.querySelector('[data-directive-composer]')),
        };
      })()`);
    const readComposer = () =>
      evalJS(`(() => {
        const box = document.querySelector('[data-directive-composer]');
        if (!box) return null;
        const area = box.querySelector('textarea[aria-label="下发内容"]');
        const receipt = box.querySelector('[data-directive-receipt]');
        const alert = box.querySelector('[role="alert"]');
        const group = (label) => [...(box.querySelectorAll('[role="group"][aria-label="' + label + '"] button') ?? [])].map((b) => b.textContent.trim());
        const send = [...box.querySelectorAll('button')].find((b) => ['发送', '创建指令', '停并换向', '处理中…'].includes(b.textContent.trim()));
        return {
          once: group('下发时效'),
          target: group('下发对象'),
          hasTextField: Boolean(area),
          textareaValue: area ? area.value : null,
          textareaMaxLength: area ? area.maxLength : null,
          sendLabel: send ? send.textContent.trim() : '',
          sendDisabled: send ? send.disabled : null,
          hint: box.querySelector('#composer-hint')?.innerText ?? '',
          receipt: receipt ? receipt.innerText : '',
          error: alert ? alert.innerText : '',
          persistentSet: Boolean(box.querySelector('[data-instruction-set]')),
        };
      })()`);

    // ---- A. the 图谱 dock is an entry point, not the composer --------------
    await call('Page.navigate', { url: graphURL });
    await waitJS('Boolean(document.querySelector(\'button[aria-label="下发"]\'))', '图谱右侧下发入口');
    await pause(400);
    check('图谱页初始没有 [data-graph-chat]', !(await evalJS('Boolean(document.querySelector("[data-graph-chat]"))')), '不存在', await evalJS('Boolean(document.querySelector("[data-graph-chat]"))'));

    await clickSelector('button[aria-label="下发"]');
    await waitJS('Boolean(document.querySelector("[data-graph-chat]"))', '右侧通信入口面板');
    await pause(500);
    const dock = await readDock();
    check(
      'dock 只作为入口：有「通信」「打开通信页」和关闭按钮',
      Boolean(dock && dock.text.includes('通信') && dock.openConversation && dock.hasClose),
      '通信 + 打开通信页 + 关闭',
      dock && { openConversation: dock.openConversation, hasClose: dock.hasClose, text: dock.text.split('\n').slice(0, 3) },
    );
    check(
      'dock 里不再有下发输入框（composer 不在图谱面板）',
      Boolean(dock && !dock.hasTextarea && !dock.hasComposer),
      '无 textarea / 无 [data-directive-composer]',
      dock && { hasTextarea: dock.hasTextarea, hasComposer: dock.hasComposer },
    );
    check(
      'dock 沿用共享宽度：默认 360px 且贴右',
      Boolean(dock && dock.width === 360 && dock.right >= 8 && dock.right <= 40),
      '宽 360 / 右侧留白 8..40',
      dock && { width: dock.width, right: dock.right, top: dock.top },
    );
    const dockShot = await shot('graph-chat-dock.png');

    // Close and reopen: the toggle must be a real switch, not one-shot.
    await clickSelector('button[aria-label="关闭通信入口"]');
    await waitJS('!document.querySelector("[data-graph-chat]")', '入口面板关闭');
    await clickSelector('button[aria-label="下发"]');
    await waitJS('Boolean(document.querySelector("[data-graph-chat]"))', '入口面板重新打开');
    await clickButtonIn('[data-graph-chat]', '打开通信页');
    await waitJS('Boolean(document.querySelector("[data-directive-composer]"))', '通信页的下发输入框');
    check('「打开通信页」把操作员带到通信页且关闭入口面板', !(await evalJS('Boolean(document.querySelector("[data-graph-chat]"))')), 'dock 关闭', await evalJS('Boolean(document.querySelector("[data-graph-chat]"))'));

    // ---- B. the unified composer on the 通信 page --------------------------
    await pause(600);
    const blank = await readComposer();
    check(
      '通信页有统一下发框：时效两组 + 对象三选 + 4000 字输入 + 发送按钮',
      Boolean(
        blank &&
          blank.once.join('/') === '一次性/持续' &&
          blank.target.join('/') === '指挥官/全部执行者/某个意图' &&
          blank.hasTextField &&
          blank.textareaMaxLength === 4000 &&
          blank.sendLabel === '发送',
      ),
      '时效 2 / 对象 3 / 4000 / 发送',
      blank && { once: blank.once, target: blank.target, max: blank.textareaMaxLength, send: blank.sendLabel },
    );
    check('默认对象为指挥官，空内容不能发送', Boolean(blank && blank.sendDisabled === true), 'disabled=true', blank && blank.sendDisabled);
    check(
      '默认提示说明一次性对象与在跑执行者数量',
      Boolean(blank && blank.hint.includes('只影响指挥官下一轮决策') && blank.hint.includes('当前在跑执行者 1 个')),
      '含「只影响指挥官下一轮决策」「当前在跑执行者 1 个」',
      blank && blank.hint,
    );

    // B1. 全部执行者: one POST per directive, a broadcast receipt with the
    // per-recipient failure, and the draft cleared only after the server答.
    await clickButtonIn('[data-directive-composer] [role="group"][aria-label="下发对象"]', '全部执行者');
    await pause(150);
    const broadcastHint = (await readComposer()).hint;
    check('切到「全部执行者」后提示改为固定收件名单', broadcastHint.includes('一次性发给当前在办的执行者'), '含「一次性发给当前在办的执行者」', broadcastHint);
    const messageBroadcast = '请优先验证 /api/search 的 JSON 接口与 /search 回显是否可复现，并附证据；内网 10.0.0.10 不在授权范围，不要触碰。';
    check('输入内容生效（约 60 字，含 URL）', (await typeInto({ selector: '[data-directive-composer] textarea[aria-label="下发内容"]', text: messageBroadcast })) === messageBroadcast, messageBroadcast, await evalJS('document.querySelector(\'[data-directive-composer] textarea[aria-label="下发内容"]\').value'));
    await clickButtonIn('[data-directive-composer]', '发送');
    await waitJS('Boolean(document.querySelector("[data-directive-composer] [data-directive-receipt]"))', '群发回执');
    await pause(300);
    const sentBroadcast = directives[0] || {};
    check(
      'a1. 捕获请求 target=executors / persistent=false / steer=false',
      sentBroadcast.target === 'executors' && sentBroadcast.persistent === false && sentBroadcast.steer === false,
      'executors/false/false',
      { target: sentBroadcast.target, persistent: sentBroadcast.persistent, steer: sentBroadcast.steer },
    );
    check('a2. 捕获请求正文一致', sentBroadcast.text === messageBroadcast, messageBroadcast, sentBroadcast.text);
    check('a3. 捕获请求 idempotency_key 非空', typeof sentBroadcast.idempotency_key === 'string' && sentBroadcast.idempotency_key.length > 0, '非空字符串', sentBroadcast.idempotency_key);
    const afterBroadcast = await readComposer();
    check(
      'a4. 群发回执列出人数与逐项失败原因',
      afterBroadcast.receipt.includes('群发已登记：2 个执行者，1 个未成功') && afterBroadcast.receipt.includes('in_done_1：已结束/停放的意图不会收到'),
      '群发已登记 2/1 + 逐项原因',
      afterBroadcast.receipt,
    );
    check('a5. 服务端成功后清空输入框', afterBroadcast.textareaValue === '', '', afterBroadcast.textareaValue);

    // B2. 指挥官 one-shot, via Enter (the composer's own send shortcut).
    await clickButtonIn('[data-directive-composer] [role="group"][aria-label="下发对象"]', '指挥官');
    await pause(150);
    const messageCommander = '收到，先只做只读确认；/api 白名单与 XFF 暂不再试探。';
    await typeInto({ selector: '[data-directive-composer] textarea[aria-label="下发内容"]', text: messageCommander });
    await pressEnter('[data-directive-composer] textarea[aria-label="下发内容"]');
    await waitJS(
      '(() => { const r = document.querySelector("[data-directive-composer] [data-directive-receipt]"); return Boolean(r && r.innerText.includes("一次性提示已发给指挥官")); })()',
      '指挥官回执',
    );
    await pause(300);
    const sentCommander = directives[1] || {};
    const afterCommander = await readComposer();
    check('b1. 捕获请求 target=commander', sentCommander.target === 'commander' && sentCommander.persistent === false, 'commander/false', { target: sentCommander.target, persistent: sentCommander.persistent });
    check('b2. 捕获请求正文一致', sentCommander.text === messageCommander, messageCommander, sentCommander.text);
    check(
      'b3. 回执说明只影响指挥官且不建立持久指令',
      afterCommander.receipt.includes('一次性提示已发给指挥官：已排队，对方下一次运行可见') && afterCommander.receipt.includes('不建立持久指令'),
      '指挥官 + 已排队 + 不建立持久指令',
      afterCommander.receipt,
    );

    // B3. Transport failure: the same content must reuse the same idempotency
    // key, so a retry cannot create a second row.
    backpressure = true;
    const messageRetry = '队列满时这条也要能安全重试。';
    await typeInto({ selector: '[data-directive-composer] textarea[aria-label="下发内容"]', text: messageRetry });
    await clickButtonIn('[data-directive-composer]', '发送');
    await waitJS('Boolean(document.querySelector("[data-directive-composer] [role=alert]"))', 'backpressure 错误');
    const failed = await readComposer();
    check('c1. 429 回执码转成可操作的错误文案', failed.error.includes('message_backpressure') && failed.error.includes('服务端没有丢弃'), '含 message_backpressure 与重试说明', failed.error);
    const failedKey = directives[2] && directives[2].idempotency_key;
    backpressure = false;
    await clickButtonIn('[data-directive-composer]', '发送');
    await waitJS('Boolean(document.querySelector("[data-directive-composer] [data-directive-receipt]"))', '重试后的回执');
    await pause(300);
    const retried = directives[3] || {};
    check('c2. 同一内容重试复用同一幂等键', Boolean(failedKey) && retried.idempotency_key === failedKey, failedKey, retried.idempotency_key);
    check('c3. 失败后输入框保留，成功后清空', retried.text === messageRetry && (await readComposer()).textareaValue === '', '保留→清空', (await readComposer()).textareaValue);

    // B4. 某个意图: one-shot list only offers directions that can still take a
    // prompt; a running direction offers the steer switch.
    await clickButtonIn('[data-directive-composer] [role="group"][aria-label="下发对象"]', '某个意图');
    await pause(150);
    await clickSelector('[data-directive-composer] [aria-label="选择下发意图"]');
    await waitJS('document.querySelectorAll(\'[role="option"]\').length > 0', '意图下拉');
    const optionTexts = await evalJS('[...document.querySelectorAll(\'[role="option"]\')].map((o) => o.textContent.trim())');
    check('d1. 一次性提示只列未收尾意图', optionTexts.some((t) => t.includes('侦察意图')) && !optionTexts.some((t) => t.includes('已收尾意图')), ['侦察意图'], optionTexts);
    const optionPoint = await rectOf('[...document.querySelectorAll(\'[role="option"]\')].find((o) => o.textContent.includes("侦察意图"))');
    if (!optionPoint) throw new Error('direction option not found');
    await clickPoint(optionPoint);
    await waitJS('Boolean(document.querySelector(\'[data-directive-composer] [aria-label="立即停并换向"]\'))', '换向勾选框');
    await pause(150);
    const steering = await readComposer();
    check('d2. 选中运行中的意图后出现「立即停并换向」', Boolean(steering && steering.hint.includes('一次性提示只发给这个意图')), '含「一次性提示只发给这个意图」', steering.hint);
    const messageSteer = '停掉字典铺路，收窄到登录口做单变量差分。';
    await typeInto({ selector: '[data-directive-composer] textarea[aria-label="下发内容"]', text: messageSteer });
    await clickSelector('[data-directive-composer] [aria-label="立即停并换向"]');
    await pause(150);
    const steerHint = (await readComposer()).hint;
    check('d3. 勾选换向后提示说明会停掉当前 run', steerHint.includes('会停掉当前 run'), '含「会停掉当前 run」', steerHint);
    await clickButtonIn('[data-directive-composer]', '停并换向');
    await waitJS('Boolean(document.querySelector("[data-directive-composer] [data-directive-receipt]"))', '换向回执');
    await pause(300);
    const sentSteer = directives[4] || {};
    const afterSteer = await readComposer();
    check(
      'd4. 换向请求 target=intent / intent_id / steer=true',
      sentSteer.target === 'intent' && sentSteer.intent_id === 'in_run_1' && sentSteer.steer === true && sentSteer.persistent === false,
      'intent/in_run_1/true/false',
      { target: sentSteer.target, intent_id: sentSteer.intent_id, steer: sentSteer.steer },
    );
    check('d5. 换向回执带新 run 且声明不建立持久指令', afterSteer.receipt.includes('已换向') && afterSteer.receipt.includes('r_steer_1') && afterSteer.receipt.includes('不建立持久指令'), '已换向 + r_steer_1', afterSteer.receipt);
    const composerShot = await shot('graph-chat-composer.png');

    // B5. 持续到撤销: the lifetime switch replaces the one-shot targets, sends
    // persistent=true and echoes the standing instruction set.
    await clickButtonIn('[data-directive-composer] [role="group"][aria-label="下发时效"]', '持续');
    await pause(200);
    const persistTargets = (await readComposer()).target;
    check('e1. 切到持续后对象剩「整个项目 / 某个意图」', persistTargets.join('/') === '整个项目/某个意图', ['整个项目', '某个意图'], persistTargets);
    await clickButtonIn('[data-directive-composer] [role="group"][aria-label="下发对象"]', '整个项目');
    await pause(150);
    check('e2. 持续模式显示现有指令集合', Boolean((await readComposer()).persistentSet), '有 [data-instruction-set]', (await readComposer()).persistentSet);
    const messageInstruction = '后续所有意图默认只读，除操作员明确授权外不得写入目标数据。';
    await typeInto({ selector: '[data-directive-composer] textarea[aria-label="下发内容"]', text: messageInstruction });
    await clickButtonIn('[data-directive-composer]', '创建指令');
    await waitJS('Boolean(document.querySelector("[data-directive-composer] [data-directive-receipt]"))', '持久指令回执');
    await pause(400);
    const sentInstruction = directives[5] || {};
    const afterInstruction = await readComposer();
    check(
      'e3. 持久指令请求 persistent=true / target=project / steer=false',
      sentInstruction.persistent === true && sentInstruction.target === 'project' && sentInstruction.steer === false,
      'true/project/false',
      { persistent: sentInstruction.persistent, target: sentInstruction.target, steer: sentInstruction.steer },
    );
    check('e4. 回执说明版本与影响范围', afterInstruction.receipt.includes('持久指令已保存（v1，整个项目）'), '持久指令已保存（v1，整个项目）', afterInstruction.receipt);
    check(
      'e5. 成功后切到持久指令页签并能看到刚创建的指令',
      (await evalJS('document.body.innerText.includes(' + JSON.stringify(messageInstruction) + ')')) &&
        (await evalJS('document.body.innerText.includes("持久指令")')),
      '页签含刚创建的指令正文',
      (await evalJS('document.body.innerText')).slice(0, 200),
    );
    const persistentShot = await shot('graph-chat-persistent.png');

    // ---- artifacts ---------------------------------------------------------
    check('无浏览器运行时异常', errors.length === 0, [], errors);
    check(
      '三张截图 1440x1000 且非空白',
      [dockShot, composerShot, persistentShot].every((s) => s.size && s.size.width === 1440 && s.size.height === 1000 && s.bytes > 20000),
      '1440x1000 / > 20KB',
      [dockShot, composerShot, persistentShot].map((s) => [s.size, s.bytes]),
    );

    const dump = [];
    dump.push('图谱右侧下发入口 + 通信页统一下发框（离线 mock，headless Chrome 1440x1000）');
    dump.push('图谱 URL: ' + graphURL);
    dump.push('通信 URL: ' + messagesURL);
    dump.push('dock: width=' + (dock ? dock.width : '?') + 'px, right=' + (dock ? dock.right : '?') + ', top=' + (dock ? dock.top : '?'));
    dump.push('dock 文案: ' + (dock ? dock.text.split('\n').slice(0, 4).join(' | ') : '?'));
    dump.push('dock 内输入框: ' + (dock && dock.hasTextarea ? '有' : '无') + '；composer: ' + (dock && dock.hasComposer ? '有' : '无'));
    dump.push('');
    dump.push('mock 捕获的 POST /p/fixture/api/directives:');
    directives.forEach((body, i) => dump.push('[' + (i + 1) + '] ' + JSON.stringify(body)));
    dump.push('');
    dump.push('群发回执:');
    (afterBroadcast.receipt || '').split('\n').forEach((line) => dump.push('  ' + line));
    dump.push('');
    dump.push('指挥官回执:');
    (afterCommander.receipt || '').split('\n').forEach((line) => dump.push('  ' + line));
    dump.push('');
    dump.push('持久指令回执:');
    (afterInstruction.receipt || '').split('\n').forEach((line) => dump.push('  ' + line));
    fs.writeFileSync(path.join(out, 'graph-chat-dom.txt'), dump.join('\n') + '\n', 'utf8');

    const result = {
      graphURL,
      messagesURL,
      checks,
      passed: checks.every((c) => c.passed),
      screenshots: [dockShot, composerShot, persistentShot],
      directives,
      receipts: {
        broadcast: afterBroadcast.receipt,
        commander: afterCommander.receipt,
        persistent: afterInstruction.receipt,
      },
      dock: dock ? { width: dock.width, right: dock.right, hasTextarea: dock.hasTextarea, hasComposer: dock.hasComposer } : null,
      domDump: '.build/ui-qa/graph-chat-dom.txt',
      runtimeErrors: errors,
    };
    const lines = [
      '图谱: ' + graphURL,
      '通信: ' + messagesURL,
      ...checks.map((c) => (c.passed ? 'PASS ' : 'FAIL ') + c.name),
      '',
      '截图:',
      ...result.screenshots.map((s) => '  ' + s.path + ' ' + (s.size ? s.size.width + 'x' + s.size.height : '?') + ' ' + s.bytes + 'B'),
      'DOM: .build/ui-qa/graph-chat-dom.txt',
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    assert.ok(result.passed, '下发入口与通信页验收未通过: ' + JSON.stringify(checks.filter((c) => !c.passed)));
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
})().catch((e) => {
  process.stderr.write(e.stack + '\n');
  process.exitCode = 1;
});
