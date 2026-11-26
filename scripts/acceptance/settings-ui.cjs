// Offline settings acceptance for the single model gateway console.
// All page HTTP responses are synthetic; no Saker service, model provider,
// project data or saved credential is used. The sibling console-gallery.cjs
// renders screenshots (not committed); this script is the behavioral regression
// for 设置.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.SAKER_SETTINGS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa'); fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'settings-chrome-'));
const chrome = spawn(process.env.SAKER_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
  '--user-data-dir=' + profile, 'about:blank',
], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const at = '2026-11-17T10:00:00+08:00';

// One instance-level gateway connection: protocol + endpoint + a single key. The
// key is write-only; api_key_set tells the form a value is stored.
let connection = { base_url: 'https://gateway.example.test/v1', model: 'example-model', provider: 'openai',
  context_window: 128000, max_tokens: 0, api_key_set: true, path: '~/.cyber/cyber.yaml' };
let runtime = { auxiliary_concurrency: 1, max_auxiliary_concurrency: 8 };
const project = (id, name) => ({ id, name, created: at, intents: 0, facts: 0, evidence: 0, running: 0,
  max_runs: null, max_runs_effective: 4, round_timeout: '', premise_policy: 'verified_only' });
let projects = [project('p0', 'Touch'), project('p1', '第二个项目')];
const saveCalls = [], checkCalls = [], runtimeSaves = [], projectSaves = [];
let modelListCalls = 0;

function settingsView() {
  return { ...connection, configured: Boolean(connection.base_url && connection.model && connection.api_key_set) };
}
function projectIndex() {
  return { projects, max_runs: 4, round_timeout_seconds: 900 };
}
function api(url, request) {
  const body = request.postData ? JSON.parse(request.postData) : {};
  const p = url.pathname;
  if (p === '/api/settings' && request.method === 'GET') return settingsView();
  if (p === '/api/settings' && request.method === 'PUT') {
    saveCalls.push(body);
    connection = { ...connection, ...body, api_key_set: connection.api_key_set || Boolean(body.api_key) };
    return settingsView();
  }
  if (p === '/api/settings/check') {
    checkCalls.push(1);
    return { probed: true, available: true, returned_model: 'gateway-model-x', ms: 9 };
  }
  if (p === '/api/models') { modelListCalls++; return { models: ['example-model'], base_url: connection.base_url, provider: connection.provider }; }
  if (p === '/api/runtime-settings' && request.method === 'GET') return runtime;
  if (p === '/api/runtime-settings' && request.method === 'PUT') {
    runtimeSaves.push(body);
    runtime = { ...runtime, auxiliary_concurrency: body.auxiliary_concurrency };
    return runtime;
  }
  if (p === '/api/projects' && request.method === 'GET') return projectIndex();
  const hit = /^\/api\/projects\/([^/]+)$/.exec(p);
  if (hit && request.method === 'PUT') {
    projectSaves.push(body);
    if (body.round_timeout === 'bad') return { code: 400, value: { error: '时长不合法' } };
    const row = projects.find(item => item.id === hit[1]);
    Object.assign(row, body, { max_runs_effective: body.max_runs ?? 4 });
    return row;
  }
  return {};
}

(async () => {
  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let i = 0; i < 100 && !fs.existsSync(portFile); i++) await pause(100);
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find(page => page.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
    let seq = 0;
    const pending = new Map(), errors = [], checks = [];
    const call = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++seq, timer = setTimeout(() => { pending.delete(id); reject(new Error('CDP timeout ' + method)); }, 8000);
      pending.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject });
      ws.send(JSON.stringify({ id, method, params }));
    });
    ws.addEventListener('message', async event => {
      const msg = JSON.parse(event.data);
      if (msg.id) { const p = pending.get(msg.id); if (p) { pending.delete(msg.id); msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result); } return; }
      if (msg.method === 'Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      if (msg.method !== 'Fetch.requestPaused') return;
      const { requestId, request } = msg.params, url = new URL(request.url);
      try {
        let body, type = 'application/json', code = 200;
        if (url.pathname.startsWith('/static/')) {
          const file = path.join(webRoot, url.pathname.slice('/static/'.length));
          if (fs.existsSync(file)) { body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream'; }
          else { body = Buffer.from('not found'); code = 404; errors.push('Missing asset ' + file); }
        } else if (url.pathname.includes('/api/')) {
          const result = api(url, request);
          code = result.code || 200;
          body = Buffer.from(JSON.stringify(result.value || result));
        } else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
        await call('Fetch.fulfillRequest', { requestId, responseCode: code, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
      } catch (error) { errors.push(error.message); }
    });
    await call('Page.enable'); await call('Runtime.enable');
    await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let i = 0; i < 100; i++) { if (await evaluate('Boolean(' + expression + ')')) return; await pause(75); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText')); };
    const text = value => wait('document.body.innerText.includes(' + JSON.stringify(value) + ')');
    const click = async label => evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&(b.textContent.trim()===${JSON.stringify(label)}||b.getAttribute('aria-label')===${JSON.stringify(label)}));if(!b)throw Error('Missing button '+${JSON.stringify(label)});b.click()})()`);
    const clickIn = async (cardText, label) => evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&b.textContent.trim()===${JSON.stringify(label)}&&((b.closest('.saker-panel')||{}).textContent||'').includes(${JSON.stringify(cardText)}));if(!b)throw Error('Missing button '+${JSON.stringify(label)}+' in '+${JSON.stringify(cardText)});b.click()})()`);
    const fill = async (label, value) => evaluate(`(()=>{const e=[...document.querySelectorAll('input')].find(e=>e.getAttribute('aria-label')===${JSON.stringify(label)});if(!e)throw Error('Missing input '+${JSON.stringify(label)});Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    const check = (name, passed, actual) => { checks.push({ name, passed }); assert.ok(passed, name + (actual === undefined ? '' : ': ' + JSON.stringify(actual))); };
    const shot = async name => { const s = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(s.data, 'base64')); };

    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1050, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: 'http://saker.test/g/?view=settings' });
    await text('统一模型连接');
    await text('复核与报告并发');
    check('page explains the one shared gateway', await evaluate("document.body.innerText.includes('指挥官、执行者和复核共用此入口')"));
    check('reading settings issues no inference or model-list request', checkCalls.length === 0 && modelListCalls === 0);
    check('form shows the saved single connection without echoing the key',
      await evaluate("document.querySelector('input[aria-label=\"接口地址\"]').value==='https://gateway.example.test/v1'&&document.querySelector('input[aria-label=\"模型名称\"]').value==='example-model'&&document.querySelector('input[aria-label=\"协议\"]').value==='openai'&&document.querySelector('input[aria-label=\"上下文窗口\"]').value==='128000'&&document.querySelector('input[aria-label=\"API Key\"]').value===''&&document.body.innerText.includes('留空保留已保存的 Key')"));

    await click('检测连接');
    await text('连接成功；响应模型：gateway-model-x。');
    check('connectivity check runs exactly once, only on the explicit click', checkCalls.length === 1 && modelListCalls === 0);

    await fill('API Key', 'fixture-new-key');
    await fill('模型名称', 'gateway-model-y');
    check('connectivity stays disabled while the draft is dirty', await evaluate("[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='检测连接').disabled"));
    await click('保存并应用');
    await text('已保存模型连接');
    check('save sends the one connection (endpoint, key, window) and nothing else',
      saveCalls.length === 1 && saveCalls[0].model === 'gateway-model-y' && saveCalls[0].api_key === 'fixture-new-key' && saveCalls[0].context_window === 128000, saveCalls[0]);
    check('saving does not fire a connectivity request', checkCalls.length === 1);
    await fill('上下文窗口', '-1');
    await click('保存并应用');
    await text('上下文窗口必须是非负整数（留空交给网关）');
    check('invalid window never reaches the server', saveCalls.length === 1);
    await fill('上下文窗口', '128000');
    await shot('settings-model-connection-desktop');

    await fill('复核与报告并发', '0');
    await click('保存并发');
    await text('并发必须是 1–8 的整数');
    check('runtime gate rejects out-of-range values locally', runtimeSaves.length === 0);
    await fill('复核与报告并发', '2');
    await click('保存并发');
    await text('已保存：之后发起的复核/报告按新并发执行');
    check('runtime gate persists one instance value', runtimeSaves.length === 1 && runtimeSaves[0].auxiliary_concurrency === 2, runtimeSaves[0]);

    await click('项目设置');
    await text('Touch');
    check('project tab lists every project', await evaluate("document.body.innerText.includes('Touch')&&document.body.innerText.includes('第二个项目')"));
    check('project cards start from the inherited empty values',
      await evaluate("document.querySelector('input[aria-label=\"Touch 的执行者并发\"]').value===''&&document.querySelector('input[aria-label=\"Touch 的指挥官单轮时限\"]').value===''"));
    await fill('Touch 的执行者并发', '3');
    await fill('第二个项目 的执行者并发', '5');
    await clickIn('Touch', '保存项目设置');
    await text('已保存。简报与约束用于之后的派发和决策');
    check('saving one project sends only its own override', JSON.stringify(projectSaves[0]) === '{"max_runs":3}', projectSaves[0]);
    check('saving one project preserves the other draft',
      await evaluate("document.querySelector('input[aria-label=\"第二个项目 的执行者并发\"]').value==='5'&&!document.querySelector('input[aria-label=\"第二个项目 的执行者并发\"]').disabled"));
    await fill('Touch 的指挥官单轮时限', 'bad');
    await clickIn('Touch', '保存项目设置');
    await text('保存失败：时长不合法');
    check('server validation error keeps the failed draft', await evaluate("document.querySelector('input[aria-label=\"Touch 的指挥官单轮时限\"]').value==='bad'"));
    await clickIn('Touch', '撤销改动');
    check('undo restores the saved baseline', await evaluate("document.querySelector('input[aria-label=\"Touch 的指挥官单轮时限\"]').value===''"));
    await shot('settings-projects-desktop');

    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await click('模型连接');
    await pause(200);
    check('model settings fit narrow screens', await evaluate('document.documentElement.scrollWidth<=innerWidth'));
    await shot('settings-model-connection-mobile');
    check('no browser runtime exceptions', errors.length === 0, errors);
    process.stdout.write(JSON.stringify({ passed: checks.length, checks, saveCalls, checkCalls, runtimeSaves, projectSaves, errors }) + '\n');
  } finally {
    if (ws) ws.close();
    if (process.platform === 'win32') spawn('taskkill', ['/pid', String(chrome.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    else chrome.kill();
  }
})().catch(error => { process.stderr.write(error.stack + '\n'); process.exitCode = 1; });
