// Offline upgrade acceptance. Every HTTP response is synthetic;
// no Saker service, saved project, model or credential is used.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.join(root, 'internal/web/static');
const out = path.join(root, '.build/ui-qa');
fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'upgrade-chrome-'));
const chrome = spawn(process.env.SAKER_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));

const stamp='2026-11-17T10:00:00Z';
const goal={id:'g_abcdefgh23',statement:'完成虚构授权验收',met:false,start:'隔离测试',created_at:stamp};
const intent={id:'in_abcdefgh23',title:'隔离执行者',description:'仅本地假模型测试',status:'done',attempts:1,created_at:stamp,updated_at:stamp,node:'local'};
const run={id:'r_abcdefgh23',intent_id:intent.id,status:'done',node:'local',started_at:stamp,ended_at:'2026-11-17T10:02:00Z',events:85,tokens:1234,model:'fixture',native_subagent_calls:1,native_subagent_duration_ms:400,infos:['原生 subagent 调用由父会话管理']};
const message={id:'m_abcdefgh23',seq:1,from_participant_id:'commander',to_participant_id:'operator',text:'请核对隔离测试结果',status:'accepted',durable:true,created_at:stamp,updated_at:stamp};
const board={generated_at:stamp,case:'fixture',intents:{total:1,pending:0,claimed:0,running:0,waiting:0,done:1,failed:0,parked:0},facts:{total:4,with_evidence:1,verified:1,notes:2,reviewable_unverified:1,by_kind:{note:2,vuln:2},by_origin:{}},evidence:1,runs:{total:1,running:0,done:1,failed:0,canceled:0},goals:{total:1,met:0,closed:0,open:[goal]},open_intents:[],waiting_intents:[],running_intents:[],recent_facts:[],done_intents:[intent.id],frontier:[],project_paused:true,project_pause_kind:'manual_pause',decider:{enabled:true,model:'fixture',rounds:3,tokens_total:1234,stalled:0,paused:true,context_percent:91,handoff_missing:2,handoff_released:true,no_action_rounds:3,pending_attention:[],last_attention:[],activity:{phase:'paused'}}};
let nodes=[{id:'local',transport:'aop',endpoint:'http://127.0.0.1:18766',agent:'local-agent',name:'local scan',bound:true,default:true,project_default:true,online:true,ready:true,terminal:true},{id:'scan_worker',transport:'aop',endpoint:'http://127.0.0.1:18766',agent:'worker-agent',name:'second scan',bound:false,default:false,project_default:false,online:true,ready:true,terminal:true,model:'fixture'}];
const events=[{kind:'input',event_id:'input',at:stamp,text:'仅用于界面验收的完整输入：请检查已授权的本地假服务。'}, {kind:'turn_started',event_id:'turn',turn_id:'turn1',at:stamp}];
for(let i=0;i<40;i++){
 const at=new Date(Date.parse(stamp)+(i+1)*1000).toISOString();
 events.push({kind:'tool_call',event_id:'call-'+i,turn_id:'turn1',at,tool:'bash',call_id:'c'+i,text:'bash',raw:{tool_call:{id:'c'+i,name:'bash',arguments:{data:Buffer.from(JSON.stringify({command:'echo 测试参数 '+i})).toString('base64')}}}});
 events.push({kind:'tool_result',event_id:'result-'+i,turn_id:'turn1',at,tool:'bash',call_id:'c'+i,ok:i!==0,text:i===0?'fixture failure':'fixture success',duration_ms:10,raw:{tool_result:{call_id:'c'+i,is_error:i===0,output:[{text:{text:'原始返回 '+i}}]}}});
}
events.push({kind:'turn_ended',event_id:'end',turn_id:'turn1',at:'2026-11-17T10:02:00Z',stop_reason:'completed',ok:true});
async function api(url,request){
 const suffix=url.pathname.split('/api/')[1];
 if(suffix?.endsWith('/events'))return {events,total:events.length};
 if(suffix==='nodes'&&request.method==='PUT'){
  const body=JSON.parse(request.postData);nodes=nodes.map(n=>({...n,bound:body.nodes.includes(n.id),project_default:n.id===body.default}));
 }
 switch(suffix){
 case 'projects':return {projects:[{id:'fixture',name:'升级验收',intents:1,facts:4,evidence:1,running:0,created:stamp,max_runs_effective:4}]};
 case 'board':return board;
 case 'goals':return {goals:[goal]};
 case 'intents':return {intents:[intent]};
 case 'runs':return {runs:[run]};
 case 'participants':return {participants:[{id:'commander',title:'指挥官',role:'commander',contactable:true},{id:'pt_'+intent.id,title:'隔离执行者',intent_id:intent.id,role:'executor',contactable:false}]};
 case 'messages':return {messages:[message,{...message,id:'m_second',seq:2,to_participant_id:'pt_'+intent.id,text:'同伴资料'}]};
 case 'operator-messages':return {messages:Number(url.searchParams.get('after'))<1&&message.status!=='confirmed'?[message]:[],next:1,more:false};
 case 'operator-messages/seen':message.status='confirmed';return {read:1,through:1};
 case 'nodes':return {nodes,default:'local',project_default:nodes.find(n=>n.project_default)?.id};
 case 'nodes/join':return {args:['agent','--transport','web','--server-url','http://fixture@127.0.0.1:18766','--node-id','abcdef'],workspace:'D:/fixture/projects/fixture/cases',note:'共享项目目录和可达 API；该凭据仅用于隔离测试'};
 case 'facts':return {facts:[],total:0,has_more:false};
 case 'state':return {state:'fixture notebook',version:1};
 case 'decisions':return {decisions:[]};
 case 'playbooks':return {playbooks:[],counts:{},has_more:false,total:0};
 case 'operator-requests':return {requests:[]};
 case 'communication':return {queues:[],max_messages:50,max_bytes:65536,paused:true};
 case 'settings':return {configured:true,model_configured:true};
 default:return {};
 }
}
(async () => {
  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let i = 0; i < 100 && !fs.existsSync(portFile); i++) await pause(100);
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find(p => p.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
    let seq = 0;
    const pending = new Map(), errors = [], checks = [];
    const call = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++seq, timer = setTimeout(() => { pending.delete(id); reject(new Error('CDP timeout ' + method)); }, 8000);
      pending.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject }); ws.send(JSON.stringify({ id, method, params }));
    });
    ws.addEventListener('message', async event => {
      const msg = JSON.parse(event.data);
      if (msg.id) { const p = pending.get(msg.id); if (p) { pending.delete(msg.id); msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result); } return; }
      if (msg.method === 'Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      if (msg.method === 'Fetch.requestPaused') {
        const { requestId, request } = msg.params, url = new URL(request.url);
        try {
          let body, type = 'application/json';
          if (url.pathname.startsWith('/static/')) {
            const file = path.join(webRoot, url.pathname.slice('/static/'.length));
            body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream';
          } else if (url.pathname.includes('/api/')) body = Buffer.from(JSON.stringify(await api(url,request)));
          else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
          await call('Fetch.fulfillRequest', { requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
        } catch (e) { errors.push(e.message); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let n = 0; n < 100; n++) { if (await evaluate(`Boolean(${expression})`)) return; await pause(75); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText')); };
    const check = async (name, expression) => { const passed = await evaluate(expression); checks.push({ name, passed }); assert.ok(passed, name); };
    const shot = async name => { const s = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(s.data, 'base64')); };
    const visit = async view => call('Page.navigate', { url: 'http://saker.test/p/fixture/?view=' + view });

    await call('Emulation.setDeviceMetricsOverride',{width:1440,height:1050,deviceScaleFactor:1,mobile:false});
    const click=async label=>evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&b.textContent.trim()===${JSON.stringify(label)});if(!b)throw Error('Missing '+${JSON.stringify(label)});b.click()})()`);
    await visit('overview');
    const diagnostics = `[...document.querySelectorAll('details > summary')].find(s => s.textContent.includes('运行与投递诊断'))`;
    await wait(diagnostics);
    await evaluate(`${diagnostics}.click()`);
    await wait("document.body.innerText.includes('交接未回执 2 轮')");
    await check('diagnostics shows measured handoff pressure when opened',"[...document.querySelectorAll('details')].some(d=>d.open&&d.textContent.includes('运行与投递诊断')&&d.textContent.includes('91%'))");
    await wait("document.body.innerText.includes('收到 1 条发给操作员的消息')");
    await check('operator recipient produces toast and title badge',"document.title.startsWith('(1) ')&&document.body.innerText.includes('请核对隔离测试结果')");
    await click('管理');
    await wait("!!document.querySelector('[role=dialog]')");
    await check('goal editor explains closed versus achieved',"document.querySelector('[role=dialog]').textContent.includes('关闭表示撤回目标')&&document.querySelector('[role=dialog]').textContent.includes('保存修改')");
    await shot('upgrade-goal-editor');
    await visit('runtime&tab=nodes');
    await wait("document.body.innerText.includes('scan_worker')");
    await click('启用到项目');
    await wait("[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='设为默认')");
    await click('设为默认');
    await wait("document.body.innerText.includes('second scan')");
    await check('native second scan can be bound and selected as project default',JSON.stringify(nodes.find(n=>n.id==='scan_worker')?.bound===true&&nodes.find(n=>n.id==='scan_worker')?.project_default===true));
    await click('生成原生接入命令');
    await wait("document.body.innerText.includes('--server-url')");
    await check('join setup discloses shared workspace requirement',"document.body.innerText.includes('共享项目目录')&&document.body.innerText.includes('隐藏接入凭据')");
    await shot('upgrade-nodes');
    await visit('messages');
    await wait("document.body.innerText.includes('全部执行者')&&document.querySelectorAll('select').length>=2");
    await check('communication exposes sender recipient and executor broadcast',"document.body.innerText.includes('发送者')&&document.body.innerText.includes('收件人')&&[...document.querySelectorAll('button')].some(b=>b.textContent.trim()==='全部执行者')");
    await click('全部执行者');
    await wait("document.body.innerText.includes('固定收件名单')");
    await check('broadcast explains fixed recipients and independent receipts',"document.body.innerText.includes('固定收件名单')&&document.body.innerText.includes('部分失败')");
    await shot('upgrade-messages');
    await visit('runs&run='+run.id);
    await wait("document.querySelectorAll('article').length>30");
    await click('全部展开');
    await check('tool result cards decode native arguments and retain full raw result',"document.body.innerText.includes('echo 测试参数 0')&&document.body.innerText.includes('原始返回 0')&&document.body.innerText.includes('完整输入')");
    const before=await evaluate("(()=>{const el=document.querySelector('article').closest('[data-radix-scroll-area-viewport]');el.scrollTop=1200;el.dispatchEvent(new Event('scroll'));return el.scrollTop})()");
    assert.ok(before>500,'run stream must have real scroll space');
    await pause(100);
    await visit('messages');
    await wait("document.body.innerText.includes('收件人')");
    await visit('runs&run='+run.id);
    await wait("document.querySelectorAll('article').length>30&&document.querySelector('article [data-event-index]')");
    await pause(400);
    await check('expanded cards survive page navigation',"document.body.innerText.includes('echo 测试参数 0')");
    await check('Radix viewport restores per-run scroll',"document.querySelector('article').closest('[data-radix-scroll-area-viewport]').scrollTop>500");
    await shot('upgrade-run-stream');
    await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
    await visit('runtime&tab=nodes');
    await wait("document.body.innerText.includes('second scan')");
    await check('node setup fits mobile viewport',"document.documentElement.scrollWidth<=innerWidth+1");
    await shot('upgrade-nodes-mobile');
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(out,'upgrade-checks.json'),JSON.stringify({checks,errors},null,2));
    console.log(JSON.stringify({passed:checks.length,errors},null,2));
  } finally {
    if (ws) ws.close();
    if (process.platform === 'win32') spawn('taskkill', ['/pid', String(chrome.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    else chrome.kill();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
