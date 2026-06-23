package agent

// benchmarkIndexHTML is the benchmark management page served at GET /benchmark.
// Migrated 1:1 from config_web.cpp benchmark_html_page() (de4cc631^:src/config_web.cpp:2520-2776),
// with one addition: a "/benchmark/record" link inserted after the <h1>.
const benchmarkIndexHTML = `
<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Aiden Benchmark</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f6f7fb;padding:clamp(18px,3vw,36px);color:#273142;max-width:1180px;margin:0 auto}
h1{font-size:24px;margin-bottom:8px;letter-spacing:-.02em}
h2{font-size:15px;margin-bottom:12px;color:#111827}
.toplink{display:inline-block;font-size:13px;margin:0 0 20px;color:#2563eb;text-decoration:none}
.card{background:#fbfcff;border:1px solid #e6eaf2;border-radius:14px;padding:20px;margin-bottom:18px;box-shadow:0 10px 28px rgba(15,23,42,.06)}
select,button{font-size:14px;padding:9px 14px;border-radius:8px;border:1px solid #d7dde8}
select{min-width:min(100%,360px);background:#fff;color:#1f2937}
button{background:#2563eb;color:#fff;border:none;cursor:pointer}
button:disabled{background:#94a3b8;cursor:not-allowed}
button:hover:not(:disabled){background:#1d4ed8}
.secondary{background:#e8eef8;color:#1e3a8a}
.secondary:hover:not(:disabled){background:#dbe6f7}
.status{font-size:13px;color:#475569;background:#eef3fb;border-radius:999px;padding:6px 12px;display:inline-flex;align-items:center;min-height:30px}
.status.running{color:#92400e;background:#fef3c7}
.status.done{color:#16a34a}
table{width:100%;border-collapse:collapse;font-size:13px;margin-top:10px}
th,td{text-align:left;padding:10px 8px;border-bottom:1px solid #edf0f6;vertical-align:middle}
th{font-weight:650;color:#5b6472;background:#f8faff}
a{color:#2563eb;text-decoration:none}
a:hover{text-decoration:underline}
.pass{color:#16a34a;font-weight:600}
.fail{color:#dc2626;font-weight:600}
.not-ready{color:#94a3b8;font-size:12px}
.run-child td{background:#f9fbff;color:#475569;font-size:12px}
.run-child .child-phase{padding-left:22px;position:relative}
.run-child .child-phase:before{content:'↳';position:absolute;left:8px;color:#94a3b8}
.badge{display:inline-block;font-size:11px;background:#e0e7ff;color:#3730a3;padding:1px 6px;border-radius:4px;margin-left:6px}
.progress{margin-top:14px;font-size:13px;color:#444;display:none}
.progress-bar{height:8px;background:#e5e7eb;border-radius:999px;overflow:hidden;margin-top:6px;width:100%}
.progress-fill{height:100%;width:0;background:#2563eb;transition:width .2s}
.terminal{background:#111827;color:#d1fae5;border-radius:12px;padding:14px;font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;min-height:190px;max-height:340px;overflow:auto;white-space:pre-wrap;word-break:break-word;border:1px solid #1f2937}
textarea{width:100%;min-height:210px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;padding:10px;border:1px solid #d7dde8;border-radius:10px;resize:vertical;background:#fff}
input[type=text],input[type=number]{font-size:14px;padding:8px 12px;border-radius:8px;border:1px solid #d7dde8;background:#fff}
input[type=text]{width:260px}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:12px}
.toolbar{margin-bottom:0}
.mode-row{justify-content:space-between;gap:16px}
.mode-tabs{display:flex;gap:14px;align-items:center;flex-wrap:wrap}
.actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.control-grid{display:grid;grid-template-columns:minmax(260px,1fr) auto;gap:14px;align-items:end;margin-top:16px}
.field-label{display:block;font-size:12px;color:#64748b;margin-bottom:6px}
.inline-field{display:inline-flex;gap:8px;align-items:center}
.muted{font-size:12px;color:#888}
.selected-tags{display:none;flex-wrap:wrap;gap:8px;margin-top:8px;min-height:32px;align-items:center}
.suite-tag{display:inline-flex;align-items:center;gap:7px;max-width:100%;padding:5px 9px;border:1px solid #cdd8ec;border-radius:999px;background:#eef4ff;color:#1e3a8a;font-size:12px;font-weight:650}
.suite-tag button{padding:0;border:0;background:transparent;color:#475569;font-size:15px;line-height:1;cursor:pointer}
.suite-tag button:hover{color:#dc2626;background:transparent}
.selected-tags-empty{font-size:12px;color:#64748b}
.err{color:#dc2626;font-size:13px;white-space:pre-wrap;margin-top:8px}
.ok{color:#16a34a;font-size:13px;margin-top:8px}
.del{background:#dc2626;font-size:12px;padding:4px 8px}
.del:hover:not(:disabled){background:#b91c1c}
@media(max-width:720px){.control-grid{grid-template-columns:1fr}.actions{width:100%}button{flex:1}input[type=text],select{width:100%;min-width:0}}
</style></head><body>
<h1>Aiden Benchmark</h1>
<a class="toplink" href="/benchmark/record">📷 录入截图任务 →</a>
<div class="card">
<div class="row toolbar mode-row">
<div class="mode-tabs">
<label><input type="radio" name="mode" value="aiden" checked onchange="onModeChange()"> Aiden Native</label>
<label><input type="radio" name="mode" value="mobilegym" onchange="onModeChange()"> MobileGym</label>
<label><input type="radio" name="mode" value="skillopt" onchange="onModeChange()"> SkillOpt</label>
<span id="statusText" class="status">idle</span>
</div>
<button id="refreshBtn" class="secondary" onclick="refreshBenchmark()">Refresh</button>
</div>
<div class="control-grid">
<div id="suiteControl">
<label class="field-label" id="suiteSelectLabel" for="suiteSelect">Benchmarks</label>
<select id="suiteSelect"><option value="">Loading...</option></select>
<div id="selectedSuiteTags" class="selected-tags" aria-live="polite"></div>
</div>
<div class="actions">
<span id="mgConfig" class="inline-field" style="display:none">
<label class="muted">Parallel <input type="number" id="parallelInput" value="4" min="1" max="16" style="width:64px"></label>
</span>
<button id="startLauncherBtn" class="secondary" onclick="startMobileGymLauncher()" style="display:none">Start Launcher</button>
<button id="runBtn" onclick="startRun()">Run</button>
<button id="delBtn" class="del" onclick="deleteSuite()" style="display:none">Delete</button>
</div>
</div>
<div id="skillOptConfig" class="row" style="display:none;margin-top:12px">
<span class="inline-field"><label class="muted">Skill <select id="skillSelect"><option value="">Loading skills...</option></select></label></span>
<span class="inline-field"><label class="muted">Backend <select id="skillOptBackendSelect" onchange="syncSkillOptBackend()"><option value="device">Real device</option><option value="mobilegym">MobileGym</option></select></label></span>
<span class="inline-field"><label class="muted">Train <select id="skillOptTrainSuiteSelect"><option value="">Loading suites...</option></select></label></span>
<span class="inline-field"><label class="muted">Verification <select id="validationSuiteSelect"><option value="">Loading suites...</option></select></label></span>
<span class="inline-field"><label class="muted">Budget <input type="number" id="budgetInput" value="10" min="1" max="100" style="width:70px"></label></span>
<span class="inline-field"><label class="muted">Edit budget <input type="number" id="editBudgetInput" value="4" min="1" max="20" style="width:70px"></label></span>
<span class="inline-field"><label class="muted">Min delta <input type="text" id="minDeltaInput" value="0.03" style="width:76px"></label></span>
</div>
<div id="progressBox" class="progress">
<div class="progress-bar"><div id="progressFill" class="progress-fill"></div></div>
</div>
</div>
<div id="unitCard" class="card">
<h2>Unit Tests</h2>
<div class="muted" style="margin-bottom:12px">Direct tool-level tests. Run one platform suite, or run all unit suites under suites/unit.</div>
<div class="control-grid">
<div>
<label class="field-label" for="unitSelect">Unit suite</label>
<select id="unitSelect"><option value="">Loading...</option></select>
</div>
<div class="actions">
<button id="runUnitBtn" onclick="startRunUnit()">Run</button>
</div>
</div>
</div>
<div class="card">
<h2>Live Log</h2>
<div id="logBox" class="terminal">No benchmark log yet.</div>
</div>
<div class="card">
<h2>AI Generate Suite</h2>
<div class="muted" style="margin-bottom:8px">Describe test scenarios in natural language. One line = one task. Supports multi-step workflows, captcha handling, login requirements, etc.</div>
<textarea id="aiPrompt" placeholder="Examples (one per line for batch generation):
1. 淘宝购买上月买过的牙膏 (需要登录+历史订单)
2. 瑞幸点一杯冰的少甜生椰拿铁
3. 大众点评给烤肉店五星评价+上传3张图片
4. 验证码滑动测试
5. 关闭广告页面的X按钮
6. 微信给不存在的联系人发消息 (预期失败场景)
7. 屏幕点击准确性测试

Or single scenario: Test agent on 3 math questions (2+2, 5*3, 10-4) with multiple choice." style="min-height:160px"></textarea>
<div class="row" style="margin-top:8px">
<input type="text" id="aiSuiteName" placeholder="suite name (a-z, 0-9, _-)">
<button id="aiGenBtn" onclick="generateSuite()">Generate</button>
</div>
<div id="aiGenMsg"></div>
</div>
<div class="card">
<h2>Import Custom Suite</h2>
<div class="muted" style="margin-bottom:8px">Paste a suite JSON (or use AI Generate above). Validation runs through runner.suite.load_suite.</div>
<div class="row">
<input type="text" id="importName" placeholder="suite name (a-z, 0-9, _-)">
<button onclick="formatJson()">Format</button>
<button onclick="importSuite()">Import</button>
</div>
<textarea id="importJson" placeholder='{"name":"my_suite","tasks":[{"id":"t1","category":"single_step","description_for_judge":"...","prompt":"...","rubric":[{"id":"r1","check":"..."}],"hard_assertions":{}}]}'></textarea>
<div id="importMsg"></div>
</div>
<div class="card"><h2>History</h2>
<table><thead><tr><th>Run ID</th><th>Suite</th><th>Status</th><th>Progress</th><th>Model</th><th>Passed</th><th>Failed</th><th>Report</th></tr></thead>
<tbody id="historyBody"><tr><td colspan="8">Loading...</td></tr></tbody></table></div>
<script>
var MOBILEGYM_LOCAL_BASE='http://127.0.0.1:4174';
var MOBILEGYM_HELPER_BASE='http://127.0.0.1:4175';
var polling=null;
var suiteIndex={};
var benchmarkSuiteCount=0;
var unitSuiteCount=0;
var skillCount=0;
var skillOptTargets=[];
var skillOptTargetBySkill={};
var logPolling=null;
var lastBenchmarkStatus={status:'idle'};
var selectedSuiteKeysState=[];
function usesMobileGymLocal(){return getMode()==='mobilegym'||(getMode()==='skillopt'&&(document.getElementById('skillOptBackendSelect').value||'device')==='mobilegym')}
function benchmarkEndpoint(path){return usesMobileGymLocal()?MOBILEGYM_LOCAL_BASE+path:path}
function mobileGymLauncherMessage(e){return 'Start the Mac MobileGym launcher first: '+String(e)}
function setStartLauncherVisible(visible){document.getElementById('startLauncherBtn').style.display=visible?'inline-block':'none'}
function showMobileGymLauncherError(e){
var msg=mobileGymLauncherMessage(e);
document.getElementById('statusText').textContent='launcher offline';
document.getElementById('statusText').className='status fail';
document.getElementById('logBox').textContent=msg;
setStartLauncherVisible(true);
return msg;
}
function logPanelError(context,e){
var msg=context+': '+String(e&&e.message?e.message:e);
document.getElementById('statusText').textContent='error';
document.getElementById('statusText').className='status fail';
document.getElementById('logBox').textContent=msg;
return msg;
}
function readErrorResponse(r){
return r.text().then(function(t){
var msg=t||('HTTP '+r.status);
try{var d=JSON.parse(t);if(d&&d.error)msg=d.error}catch(_){ }
throw new Error(msg);
});
}
function jsonOrError(r){return r.ok?r.json():readErrorResponse(r)}
function updateProgress(d){
setStartLauncherVisible(false);
var box=document.getElementById('progressBox');
var fill=document.getElementById('progressFill');
var status=document.getElementById('statusText');
var total=Number(d.total||0),done=Number(d.completed||0),cur=d.current_task||'';
if(d.status==='running'&&(total||cur)){
box.style.display='block';
var label=(total?done+'/'+total:'running')+(cur?' · '+cur:'')+(d.current_attempt?' attempt '+d.current_attempt:'');
status.textContent=label;
status.className='status running';
var shown=Number(d.current||done||0);
fill.style.width=total?Math.max(0,Math.min(100,shown/total*100))+'%':'0';
}else{
box.style.display='none';
status.textContent=d.status||'idle';
status.className='status '+(d.status||'');
fill.style.width='0';
}}
function loadLog(){
fetch(benchmarkEndpoint('/benchmark/log')+'?mode='+encodeURIComponent(getMode())).then(r=>r.text()).then(function(t){
var box=document.getElementById('logBox');
var atBottom=(box.scrollTop+box.clientHeight>=box.scrollHeight-24);
box.textContent=t||'No benchmark log yet.';
if(atBottom)box.scrollTop=box.scrollHeight;
}).catch(function(e){if(getMode()==='mobilegym')showMobileGymLauncherError(e)});
}
function getMode(){
var els=document.getElementsByName('mode');
for(var i=0;i<els.length;i++){if(els[i].checked)return els[i].value}
return 'aiden';
}
function selectedSuiteKeys(){
return selectedSuiteKeysState.slice();
}
function selectedBenchmarkSuites(){return selectedSuiteKeys().map(function(key){return {key:key,item:suiteIndex[key]}}).filter(function(x){return x.item})}
function suiteLabel(key){var item=suiteIndex[key];return item?(item.name+(item.task_count?(' · '+item.task_count):'')):key}
function syncSelectedSuiteKeys(){selectedSuiteKeysState=selectedSuiteKeysState.filter(function(key){return !!suiteIndex[key]})}
function renderSelectedSuiteTags(){var wrap=document.getElementById('selectedSuiteTags');if(!wrap)return;if(getMode()==='skillopt'){wrap.style.display='none';wrap.innerHTML='';return}wrap.style.display='flex';wrap.innerHTML='';syncSelectedSuiteKeys();if(!selectedSuiteKeysState.length){var empty=document.createElement('span');empty.className='selected-tags-empty';empty.textContent=getMode()==='mobilegym'?'Select one or more suites. Run Aiden and built-in suites separately.':'Select one or more benchmark suites.';wrap.appendChild(empty);return}selectedSuiteKeysState.forEach(function(key){var tag=document.createElement('span');tag.className='suite-tag';var label=document.createElement('span');label.textContent=suiteLabel(key);var btn=document.createElement('button');btn.type='button';btn.setAttribute('aria-label','Remove '+suiteLabel(key));btn.textContent='×';btn.onclick=function(){removeSelectedSuiteKey(key)};tag.appendChild(label);tag.appendChild(btn);wrap.appendChild(tag);});}
function addSelectedSuiteKey(key){if(!key)return;var item=suiteIndex[key];if(!item)return;var current=selectedBenchmarkSuites();if(selectedSuiteKeysState.indexOf(key)<0){if(getMode()==='mobilegym'&&current.length>0&&current.some(function(x){return x.item.type!==item.type})){alert('Run Aiden and built-in MobileGym suites separately');document.getElementById('suiteSelect').value='';return}selectedSuiteKeysState.push(key)}document.getElementById('suiteSelect').value='';renderSelectedSuiteTags();syncDelBtn();syncRunButtons();}
function removeSelectedSuiteKey(key){selectedSuiteKeysState=selectedSuiteKeysState.filter(function(item){return item!==key});renderSelectedSuiteTags();syncDelBtn();syncRunButtons();}
function configureSuiteSelect(){var s=document.getElementById('suiteSelect');s.multiple=false;s.size=1;renderSelectedSuiteTags();}
function syncRunButtons(status){if(status)lastBenchmarkStatus=status;var mode=getMode();var running=lastBenchmarkStatus&&lastBenchmarkStatus.status==='running';var hasBench=mode==='skillopt'?(benchmarkSuiteCount>0&&skillCount>0&&!!document.getElementById('skillOptTrainSuiteSelect').value&&!!document.getElementById('validationSuiteSelect').value):(benchmarkSuiteCount>0&&selectedSuiteKeys().length>0);document.getElementById('runBtn').disabled=(running||!hasBench);document.getElementById('runUnitBtn').disabled=(running||unitSuiteCount===0||mode!=='aiden')}
function onModeChange(){
var mode=getMode();
var mg=mode==='mobilegym';
var skillopt=mode==='skillopt';
document.getElementById('skillOptConfig').style.display=skillopt?'flex':'none';
document.getElementById('suiteControl').style.display=skillopt?'none':'block';
document.getElementById('unitCard').style.display=mode==='aiden'?'block':'none';
document.getElementById('suiteSelectLabel').textContent=skillopt?'Train suite':'Benchmarks';
if(!skillopt)selectedSuiteKeysState=[];
configureSuiteSelect();
syncSkillOptBackend();
setStartLauncherVisible(false);
loadSuites();
if(!skillopt)loadSkills();
loadRuns();
loadStatus();
}
function load(){loadSuites();if(getMode()!=='skillopt')loadSkills();loadRuns();loadStatus()}
function refreshBenchmark(){loadSuites();if(getMode()!=='skillopt')loadSkills();loadRuns();loadStatus();loadLog()}
function loadSuites(){
var mode=getMode();
if(mode==='skillopt'){loadSkillOptTargets();return}
fetch(benchmarkEndpoint('/benchmark/suites')+'?mode='+encodeURIComponent(mode)).then(r=>r.json()).then(d=>{
var s=document.getElementById('suiteSelect');var u=document.getElementById('unitSelect');var v=document.getElementById('validationSuiteSelect');
s.innerHTML='';u.innerHTML='';v.innerHTML='';suiteIndex={};benchmarkSuiteCount=0;unitSuiteCount=0;
if(!d || !d.length){
var o=document.createElement('option');o.value='';o.textContent='(no suites)';s.appendChild(o);
var uo=document.createElement('option');uo.value='';uo.textContent='(no unit suites)';u.appendChild(uo);
var vo=document.createElement('option');vo.value='';vo.textContent='(no verification suites)';v.appendChild(vo);
configureSuiteSelect();syncDelBtn();syncRunButtons();return;
}
var groups={aiden:[],mobilegym_builtin:[]};
d.forEach(function(x){
if(mode!=='mobilegym'&&x.kind==='unit'){unitSuiteCount++;return}
(groups[x.type]||groups.aiden).push(x);benchmarkSuiteCount++;
});
function appendGroup(label,arr,target){
if(!arr.length)return;
var og=document.createElement('optgroup');og.label=label;
arr.forEach(function(x){
var o=document.createElement('option');
o.value=x.path||x.name;
var n=x.name+(x.task_count?(' ('+x.task_count+' tasks)'):'')+(x.custom?' (custom)':'');
o.textContent=n;
og.appendChild(o);
suiteIndex[x.path||x.name]=x;
});
target.appendChild(og);
}
var add=document.createElement('option');add.value='';add.textContent='Add suite...';s.appendChild(add);
appendGroup('Aiden Suites',groups.aiden,s);
if(mode==='mobilegym')appendGroup('MobileGym Built-in',groups.mobilegym_builtin,s);
if(!benchmarkSuiteCount){var bo=document.createElement('option');bo.value='';bo.textContent='(no benchmark suites)';s.appendChild(bo)}
if(mode!=='mobilegym'){
d.forEach(function(x){
if(x.kind!=='unit')return;
var o=document.createElement('option');o.value=x.path||x.name;
o.textContent=x.name+(x.custom?' (custom)':'');
u.appendChild(o);suiteIndex[x.path||x.name]=x;
});
if(!unitSuiteCount){var uo=document.createElement('option');uo.value='';uo.textContent='(no unit suites)';u.appendChild(uo)}
}
configureSuiteSelect();syncDelBtn();syncRunButtons();
}).catch(function(e){
var s=document.getElementById('suiteSelect');var u=document.getElementById('unitSelect');var v=document.getElementById('validationSuiteSelect');s.innerHTML='';u.innerHTML='';v.innerHTML='';suiteIndex={};
var o=document.createElement('option');o.value='';o.textContent=getMode()==='mobilegym'?'(Start the Mac MobileGym launcher first)':'(failed to load suites)';s.appendChild(o);
var uo=document.createElement('option');uo.value='';uo.textContent='(failed to load unit suites)';u.appendChild(uo);
var vo=document.createElement('option');vo.value='';vo.textContent='(failed to load verification suites)';v.appendChild(vo);
benchmarkSuiteCount=0;unitSuiteCount=0;configureSuiteSelect();syncDelBtn();syncRunButtons();
if(getMode()==='mobilegym')showMobileGymLauncherError(e);
});
}
function loadSkills(){
var sel=document.getElementById('skillSelect');
if(getMode()!=='skillopt'){skillCount=0;return}
fetch('/benchmark/skills').then(r=>r.json()).then(function(d){
sel.innerHTML='';skillCount=0;
if(!d||!d.length){var o=document.createElement('option');o.value='';o.textContent='(no skills)';sel.appendChild(o);loadStatus();return}
d.forEach(function(x){var o=document.createElement('option');o.value=x.name;o.textContent=x.name;sel.appendChild(o);skillCount++});
loadStatus();
}).catch(function(e){skillCount=0;sel.innerHTML='<option value="">(failed to load skills)</option>';logPanelError('Load skills failed',e)});
}
function syncSkillOptBackend(){
var mode=getMode();
var backend=document.getElementById('skillOptBackendSelect').value||'device';
document.getElementById('mgConfig').style.display=(mode==='mobilegym'||(mode==='skillopt'&&backend==='mobilegym'))?'inline-block':'none';
}
function loadSkillOptTargets(){
if(getMode()!=='skillopt')return;
fetch('/benchmark/skillopt-targets').then(jsonOrError).then(function(d){
skillOptTargets=d||[];skillOptTargetBySkill={};
skillOptTargets.forEach(function(x){skillOptTargetBySkill[x.skill]=x});
populateSkillSelectFromTargets();
syncSkillOptSuites();
loadStatus();
}).catch(function(e){
skillOptTargets=[];
skillOptTargetBySkill={};
suiteIndex={};
skillCount=0;
benchmarkSuiteCount=0;
document.getElementById('skillSelect').innerHTML='<option value="">(failed to load skills)</option>';
document.getElementById('skillOptTrainSuiteSelect').innerHTML='<option value="">(failed to load train suites)</option>';
document.getElementById('validationSuiteSelect').innerHTML='<option value="">(failed to load verification suites)</option>';
syncDelBtn();syncRunButtons();
loadStatus();
logPanelError('Load SkillOpt targets failed',e);
});
}
function populateSkillSelectFromTargets(){
var sel=document.getElementById('skillSelect');
var current=sel.value;
sel.innerHTML='';skillCount=0;
if(!skillOptTargets.length){var o=document.createElement('option');o.value='';o.textContent='(no SkillOpt targets)';sel.appendChild(o);return}
skillOptTargets.forEach(function(x){var o=document.createElement('option');o.value=x.skill;o.textContent=x.skill;sel.appendChild(o);skillCount++});
if(current&&skillOptTargetBySkill[current])sel.value=current;
}
function syncSkillOptSuites(){
var skill=document.getElementById('skillSelect').value;
var target=skillOptTargetBySkill[skill]||{};
var train=document.getElementById('skillOptTrainSuiteSelect');
var verification=document.getElementById('validationSuiteSelect');
train.innerHTML='';verification.innerHTML='';suiteIndex={};benchmarkSuiteCount=0;
function addOptions(labels,select,emptyText,countAsBenchmark){
if(!labels||!labels.length){var empty=document.createElement('option');empty.value='';empty.textContent=emptyText;select.appendChild(empty);return}
labels.forEach(function(label){var o=document.createElement('option');o.value=label;o.textContent=label.split('/').pop();select.appendChild(o);suiteIndex[label]={name:label.split('/').pop(),type:'aiden'};if(countAsBenchmark)benchmarkSuiteCount++});
}
addOptions(target.train_suites,train,'(no train suites)',true);
addOptions(target.verification_suites,verification,'(no verification suites)',false);
if(target.default_train_suite)train.value=target.default_train_suite;
if(target.default_verification_suite)verification.value=target.default_verification_suite;
syncDelBtn();
loadStatus();
}
function syncDelBtn(){
var keys=selectedSuiteKeys();
var x=keys.length===1?suiteIndex[keys[0]]:null;
document.getElementById('delBtn').style.display=(x&&x.custom)?'inline-block':'none';
}
function aidenSuiteName(item,key){
var p=(item&&item.path)||key;
var marker='/suites/';
var i=p.indexOf(marker);
if(i>=0)p=p.slice(i+marker.length);
if(p.endsWith('.json'))p=p.slice(0,-5);
return p;
}
function mobileGymSuiteName(item,key){
if(item.type!=='aiden')return item.name;
return aidenSuiteName(item,key);
}
function progressText(r){if(r.hide_totals)return '—';return r.progress||((r.totals&&r.totals.tasks)?((r.totals.tasks)+'/'+(r.totals.tasks)):'—')}
function metricText(r,key){
if(r.hide_totals)return '—';
var t=r.totals||{};
return t[key]||0;
}
function appendTextCell(tr,value,className){
var td=document.createElement('td');
if(className)td.className=className;
td.textContent=value==null?'':String(value);
tr.appendChild(td);
return td;
}
function reportCell(r){
var td=document.createElement('td');
var t=r.totals||{};
var total=Number(t.tasks||0);
if((r.status||'done')!=='done'||total<1){
var span=document.createElement('span');
span.className='not-ready';
span.textContent='Not ready';
td.appendChild(span);
return td;
}
var reportBase=(getMode()==='skillopt'&&r.__history_source==='mobilegym')?MOBILEGYM_LOCAL_BASE:(usesMobileGymLocal()?MOBILEGYM_LOCAL_BASE:'');
var reportHref=reportBase+(r.report_path?r.report_path:('/benchmark/report/'+encodeURIComponent(r.run_id)));
var link=document.createElement('a');
link.href=reportHref;
link.textContent='View';
td.appendChild(link);
return td;
}
function renderRunRow(r,isChild,parentSource){
var t=r.totals||{};var suite=r.suite||'';
var sn=isChild?(r.phase||suite.split('/').pop().replace('.json','')):suite.split('/').pop().replace('.json','');
if(parentSource&&!r.__history_source)r.__history_source=parentSource;
var tr=document.createElement('tr');
if(isChild)tr.className='run-child';
appendTextCell(tr,r.run_id);
appendTextCell(tr,sn,isChild?'child-phase':'');
appendTextCell(tr,r.status||'done');
appendTextCell(tr,progressText(r));
appendTextCell(tr,r.model||'—');
appendTextCell(tr,metricText(r,'passed'),'pass');
appendTextCell(tr,metricText(r,'failed'),'fail');
tr.appendChild(reportCell(r));
return tr;
}
function renderRuns(d,emptyText){
var tb=document.getElementById('historyBody');tb.innerHTML='';
if(!d.length){tb.innerHTML='<tr><td colspan="8">'+(emptyText||'No runs yet')+'</td></tr>';return}
d.forEach(function(r){
tb.appendChild(renderRunRow(r,false,''));
(r.children||[]).forEach(function(child){tb.appendChild(renderRunRow(child,true,r.__history_source||''))});
});
}
function isSkillOptRun(r){var id=String((r&&r.run_id)||'');var suite=String((r&&r.suite)||'');return id.indexOf('skillopt-')===0||suite.indexOf('skillopt/')===0}
function mergeRunLists(lists){var byId={};lists.forEach(function(list){(list||[]).forEach(function(r){if(!isSkillOptRun(r))return;var id=String(r.run_id||'');if(!id)return;if(!byId[id]){byId[id]=r;return}if((r.children||[]).length){byId[id].children=r.children;byId[id].__history_source=r.__history_source||byId[id].__history_source}});});return Object.keys(byId).sort().reverse().map(function(id){return byId[id]})}
function loadSkillOptRuns(){
Promise.allSettled([
fetch('/benchmark/runs').then(function(r){return r.json()}).then(function(d){return (d||[]).map(function(r){r.__history_source='board';(r.children||[]).forEach(function(c){c.__history_source='board'});return r})}),
fetch(MOBILEGYM_LOCAL_BASE+'/benchmark/runs').then(function(r){return r.json()}).then(function(d){return (d||[]).map(function(r){r.__history_source='mobilegym';(r.children||[]).forEach(function(c){c.__history_source='mobilegym'});return r})})
]).then(function(results){
var lists=results.filter(function(x){return x.status==='fulfilled'&&Array.isArray(x.value)}).map(function(x){return x.value});
renderRuns(mergeRunLists(lists),'No SkillOpt runs yet');
}).catch(function(){renderRuns([],'Failed to load SkillOpt runs')})
}
function loadRuns(){
if(getMode()==='skillopt'){loadSkillOptRuns();return}
fetch(benchmarkEndpoint('/benchmark/runs')).then(r=>r.json()).then(function(d){renderRuns(d,'No runs yet')}).catch(function(e){
var tb=document.getElementById('historyBody');
tb.innerHTML='';
var tr=document.createElement('tr');
var td=document.createElement('td');
td.colSpan=8;
td.textContent=getMode()==='mobilegym'?mobileGymLauncherMessage(e):'Failed to load runs';
tr.appendChild(td);
tb.appendChild(tr);
})}
function loadStatus(){
fetch(benchmarkEndpoint('/benchmark/status')).then(r=>r.json()).then(d=>{
syncRunButtons(d);
updateProgress(d);
loadLog();
if(d.status==='running'&&!polling)polling=setInterval(pollStatus,3000);
if(d.status==='running'&&!logPolling)logPolling=setInterval(loadLog,1000);
}).catch(function(e){syncRunButtons({status:'idle'});if(getMode()==='mobilegym')showMobileGymLauncherError(e)})}
function pollStatus(){
fetch(benchmarkEndpoint('/benchmark/status')).then(r=>r.json()).then(d=>{
syncRunButtons(d);
updateProgress(d);
loadLog();
if(d.status!=='running'){
clearInterval(polling);polling=null;
if(logPolling){clearInterval(logPolling);logPolling=null}
loadRuns();loadLog()
}
}).catch(function(e){if(polling){clearInterval(polling);polling=null}if(logPolling){clearInterval(logPolling);logPolling=null}syncRunButtons({status:'idle'});if(getMode()==='mobilegym')showMobileGymLauncherError(e)})}
function startMobileGymLauncher(){
var btn=document.getElementById('startLauncherBtn');
var controller=new AbortController();
var timer=setTimeout(function(){controller.abort()},10000);
btn.disabled=true;
document.getElementById('statusText').textContent='starting launcher';
document.getElementById('statusText').className='status running';
fetch(MOBILEGYM_HELPER_BASE+'/start',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}',signal:controller.signal}).then(jsonOrError).then(function(){
btn.disabled=false;
setStartLauncherVisible(false);
setTimeout(refreshBenchmark,1200);
}).catch(function(e){
btn.disabled=false;
logPanelError('Start launcher failed',e&&e.name==='AbortError'?'request timed out':e);
}).finally(function(){clearTimeout(timer)});
}
function startRun(){
var mode=getMode();
var payload={};
if(mode==='skillopt'){
var key=document.getElementById('skillOptTrainSuiteSelect').value;
var item=suiteIndex[key];
var skill=document.getElementById('skillSelect').value;
var validationKey=document.getElementById('validationSuiteSelect').value;
var validationItem=suiteIndex[validationKey];
if(!key){alert('Select a train suite');return}
if(!skill){alert('Select a skill');return}
if(!validationKey){alert('Select a verification suite');return}
payload.mode='skillopt';
payload.skillopt_backend=document.getElementById('skillOptBackendSelect').value||'device';
if(payload.skillopt_backend==='mobilegym')payload.board_url=location.origin;
payload.mobilegym_parallel=Number(document.getElementById('parallelInput').value)||1;
payload.skill=skill;
payload.train_suite=aidenSuiteName(item,key);
payload.validation_suite=aidenSuiteName(validationItem,validationKey);
payload.budget=Number(document.getElementById('budgetInput').value)||10;
payload.edit_budget=Number(document.getElementById('editBudgetInput').value)||4;
var minDeltaRaw=document.getElementById('minDeltaInput').value;
var minDelta=Number(minDeltaRaw);
payload.min_delta=(minDeltaRaw===''||!Number.isFinite(minDelta))?0.03:minDelta;
} else {
var selected=selectedBenchmarkSuites();
if(!selected.length){alert('Select a suite');return}
if(mode==='aiden'){
var key=selected[0].key;var item=selected[0].item;
if(selected.length>1){payload.suites=selected.map(function(x){return x.item.path||x.key});}
else{payload.suite=item.path||key;}
payload.mode='aiden';
} else {
if(selected.length>1&&selected.some(function(x){return x.item.type!==selected[0].item.type})){alert('Run Aiden and built-in MobileGym suites separately');return}
var key=selected[0].key;var item=selected[0].item;
if(selected.length>1){payload.suites=selected.map(function(x){return mobileGymSuiteName(x.item,x.key)});payload.suite_type=item.type;}
else{
payload.suite=mobileGymSuiteName(item,key);
payload.suite_type=item.type;
}
payload.mode='mobilegym';
payload.board_url=location.origin;
payload.parallel=Number(document.getElementById('parallelInput').value)||4;
}
}
document.getElementById('runBtn').disabled=true;
document.getElementById('runUnitBtn').disabled=true;
document.getElementById('statusText').textContent='running';
document.getElementById('statusText').className='status running';
fetch(benchmarkEndpoint('/benchmark/run'),{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify(payload)}).then(jsonOrError).then(function(){
loadLog();
polling=setInterval(pollStatus,3000);
if(!logPolling)logPolling=setInterval(loadLog,1000)}).catch(function(e){
document.getElementById('runBtn').disabled=false;
document.getElementById('runUnitBtn').disabled=false;
if(getMode()==='mobilegym'&&String(e).indexOf('Failed to fetch')>=0)showMobileGymLauncherError(e);else logPanelError('Start run failed',e);
});
}
function startRunUnit(){
var suite=document.getElementById('unitSelect').value;
if(!suite){alert('Select a unit suite');return}
document.getElementById('runBtn').disabled=true;
document.getElementById('runUnitBtn').disabled=true;
document.getElementById('statusText').textContent='running';
document.getElementById('statusText').className='status running';
fetch('/benchmark/run',{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({suite:suite,mode:'aiden'})}).then(jsonOrError).then(function(){
loadLog();
polling=setInterval(pollStatus,3000);
if(!logPolling)logPolling=setInterval(loadLog,1000)}).catch(function(e){
document.getElementById('runBtn').disabled=false;
document.getElementById('runUnitBtn').disabled=false;
logPanelError('Start unit run failed',e);
});
}
function generateSuite(){
var prompt=document.getElementById('aiPrompt').value.trim();
var name=document.getElementById('aiSuiteName').value.trim();
var msg=document.getElementById('aiGenMsg');msg.textContent='';msg.className='';
var btn=document.getElementById('aiGenBtn');
if(!prompt){msg.textContent='Describe your test scenario first';msg.className='err';return}
if(!name){msg.textContent='Suite name required';msg.className='err';return}
btn.disabled=true;
msg.textContent='Generating... (10-60s depending on complexity)';msg.className='';
fetch('/benchmark/suites/generate',{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({prompt:prompt,name:name})}).then(r=>r.json().then(d=>({status:r.status,d:d}))).then(function(res){
btn.disabled=false;
if(res.status>=400||!res.d.ok){msg.textContent=res.d.error||'Generation failed';msg.className='err';return}
document.getElementById('importJson').value=res.d.suite_json;
document.getElementById('importName').value=name;
document.getElementById('aiPrompt').value='';
document.getElementById('aiSuiteName').value='';
msg.textContent='Generated! Review JSON below and click Import.';msg.className='ok';
}).catch(function(e){btn.disabled=false;msg.textContent=String(e);msg.className='err'});
}
function escapeCtrlInStrings(s){
var out='',inStr=false,esc=false;
for(var i=0;i<s.length;i++){
var c=s[i];
if(esc){out+=c;esc=false;continue}
if(c==='\\'){out+=c;esc=true;continue}
if(c==='"'){inStr=!inStr;out+=c;continue}
if(inStr){
if(c==='\n')out+='\\n';
else if(c==='\r')out+='\\r';
else if(c==='\t')out+='\\t';
else if(c.charCodeAt(0)<0x20)out+='\\u00'+('0'+c.charCodeAt(0).toString(16)).slice(-2);
else out+=c;
}else{out+=c}
}
return out;
}
function formatJson(){
var msg=document.getElementById('importMsg');msg.textContent='';msg.className='';
var raw=document.getElementById('importJson').value;
if(!raw.trim()){msg.textContent='Paste JSON first';msg.className='err';return}
var obj=null,fixed=false;
try{obj=JSON.parse(raw)}catch(e1){
try{var sanitized=escapeCtrlInStrings(raw);obj=JSON.parse(sanitized);fixed=true}
catch(e2){msg.textContent='Invalid JSON: '+e1.message;msg.className='err';return}
}
document.getElementById('importJson').value=JSON.stringify(obj,null,2);
msg.textContent=fixed?'Formatted (fixed control chars in strings)':'Formatted & validated';
msg.className='ok';
}
function importSuite(){
var name=document.getElementById('importName').value.trim();
var json=document.getElementById('importJson').value;
var msg=document.getElementById('importMsg');msg.textContent='';msg.className='';
if(!name){msg.textContent='Name required';msg.className='err';return}
if(!json.trim()){msg.textContent='JSON required';msg.className='err';return}
try{JSON.parse(json)}catch(e){
try{json=escapeCtrlInStrings(json);JSON.parse(json)}
catch(e2){msg.textContent='Invalid JSON: '+e.message+' (try Format first)';msg.className='err';return}
}
fetch('/benchmark/suites/import',{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({name:name,json:json})}).then(r=>r.json().then(d=>({status:r.status,d:d}))).then(function(res){
if(res.status>=400||!res.d.ok){msg.textContent=res.d.error||'Import failed';msg.className='err';return}
msg.textContent='Imported '+res.d.name;msg.className='ok';
document.getElementById('importJson').value='';document.getElementById('importName').value='';
loadSuites();
}).catch(function(e){msg.textContent=String(e);msg.className='err'});
}
function deleteSuite(){
var p=document.getElementById('suiteSelect').value;
var x=suiteIndex[p];if(!x||!x.custom)return;
if(!confirm('Delete suite '+x.name+'?'))return;
fetch('/benchmark/suites/delete',{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({name:x.name})}).then(function(r){return r.json()}).then(function(){loadSuites()});
}
document.getElementById('suiteSelect').addEventListener('change',function(){if(getMode()==='skillopt'){syncDelBtn();syncRunButtons();return}addSelectedSuiteKey(this.value);});
document.getElementById('skillSelect').addEventListener('change',syncSkillOptSuites);
document.getElementById('skillOptTrainSuiteSelect').addEventListener('change',function(){syncDelBtn();syncRunButtons();});
load();
</script></body></html>
`

// benchmarkRecordHTML is the screenshot-task recorder served at /benchmark/record.
// Uses the same visual style as the benchmark index. All screenshot tasks are
// appended to the builtin perception_v1 suite; users only fill 2 fields
// (target name + user intent). Task IDs are auto-generated.
const benchmarkRecordHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Aiden Benchmark · 录入截图任务</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;padding:24px;color:#333;max-width:1120px;margin:0 auto}
h1{font-size:20px;margin-bottom:6px}
.back{font-size:13px;color:#2563eb;text-decoration:none;display:inline-block;margin-bottom:14px}
.back:hover{text-decoration:underline}
.subtitle{font-size:13px;color:#888;margin-bottom:14px}
.card{background:#fff;border-radius:8px;padding:16px;margin-bottom:14px;box-shadow:0 1px 3px rgba(0,0,0,.1)}
h2{font-size:15px;margin-bottom:8px}
label{display:block;font-size:13px;color:#475569;margin:10px 0 4px;font-weight:500}
input[type=text],textarea{font-size:14px;padding:8px 12px;border-radius:6px;border:1px solid #ddd;width:100%;background:#fff;font-family:inherit}
textarea{min-height:60px;resize:vertical}
button{font-size:14px;padding:8px 16px;border-radius:6px;border:none;background:#2563eb;color:#fff;cursor:pointer}
button:disabled{background:#94a3b8;cursor:not-allowed}
button:hover:not(:disabled){background:#1d4ed8}
.btn-secondary{background:#64748b}
.btn-secondary:hover:not(:disabled){background:#475569}
.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-top:8px}
.field-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.muted{font-size:12px;color:#888}
.canvas-wrap{margin:12px 0;border:1px solid #e5e7eb;border-radius:6px;display:inline-block;max-width:100%;background:#f9fafb}
canvas{display:block;cursor:crosshair;max-width:100%;height:auto}
.json-preview{background:#0f172a;color:#d1fae5;padding:12px;border-radius:6px;font:12px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre-wrap;word-break:break-word;max-height:320px;overflow:auto;border:1px solid #1e293b;margin-top:8px}
.json-preview[contenteditable=true]{background:#1e293b}
.err{color:#dc2626;font-size:13px;margin-top:6px}
.ok{color:#16a34a;font-size:13px;margin-top:6px}
.status-line{font-size:13px;color:#475569;margin-top:6px;min-height:18px}
input[type=file]{display:none}
</style></head><body>
<a href="/benchmark" class="back">← 返回 Benchmark</a>
<h1>📷 录入截图任务</h1>
<div class="subtitle">截图任务会自动追加到 <code>perception_v1</code> suite，截图存到 <code>perception/screenshots/</code></div>

<div class="card">
<h2>1. 加载截图</h2>
<div class="row">
<button id="grabBtn" onclick="grabFromDevice()">📷 从设备抓取</button>
<button class="btn-secondary" onclick="document.getElementById('fileInput').click()">📁 上传图片</button>
<input type="file" id="fileInput" accept="image/*">
<span class="muted">或 Ctrl/Cmd+V 粘贴</span>
</div>
<div class="canvas-wrap" id="canvasWrap" style="display:none">
<canvas id="canvas"></canvas>
</div>
<div class="status-line muted" id="rectInfo">加载图片后，在画面上拖拽鼠标画矩形选中目标</div>
</div>

<div class="card">
<h2>2. 描述目标</h2>
<div class="field-grid">
<div>
<label for="targetName">目标对象 (UI 元素名)</label>
<input type="text" id="targetName" placeholder="例：Settings icon">
</div>
<div>
<label for="userIntent">想让 agent 做什么</label>
<input type="text" id="userIntent" placeholder="例：打开设置 app">
</div>
</div>
</div>

<div class="card">
<h2>3. 生成 + 追加</h2>
<div class="row">
<button id="genBtn" onclick="generateTask()" disabled>✨ Generate with LLM</button>
<button id="importBtn" onclick="importTask()" disabled>追加到 perception_v1</button>
</div>
<div class="status-line" id="msg"></div>
<div class="json-preview" id="jsonPreview" contenteditable="false">（点 Generate 后显示生成的 task JSON）</div>
</div>

<script>
let img=null, rect=null, canvas, ctx, lastTaskJSON='', lastTaskId='';

function generateTaskId(targetName){
  var slug=targetName.toLowerCase().replace(/[^a-z0-9]+/g,'_').replace(/^_+|_+$/g,'');
  if(!slug) slug='task';
  var ts=Math.floor(Date.now()/1000)%10000;
  return slug+'_'+ts;
}

function setupCanvas(){
  canvas=document.getElementById('canvas');
  ctx=canvas.getContext('2d');
  var dragging=false, startX=0, startY=0;
  canvas.addEventListener('mousedown',function(e){
    var r=canvas.getBoundingClientRect();
    startX=(e.clientX-r.left)*canvas.width/r.width;
    startY=(e.clientY-r.top)*canvas.height/r.height;
    dragging=true;
  });
  canvas.addEventListener('mousemove',function(e){
    if(!dragging) return;
    var r=canvas.getBoundingClientRect();
    var x=(e.clientX-r.left)*canvas.width/r.width;
    var y=(e.clientY-r.top)*canvas.height/r.height;
    drawWithRect(startX,startY,x,y);
  });
  canvas.addEventListener('mouseup',function(e){
    if(!dragging) return;
    dragging=false;
    var r=canvas.getBoundingClientRect();
    var x=(e.clientX-r.left)*canvas.width/r.width;
    var y=(e.clientY-r.top)*canvas.height/r.height;
    rect={x1:Math.min(startX,x),y1:Math.min(startY,y),x2:Math.max(startX,x),y2:Math.max(startY,y)};
    drawWithRect(rect.x1,rect.y1,rect.x2,rect.y2);
    showRectInfo();
    document.getElementById('genBtn').disabled=false;
  });
}

function showRectInfo(){
  if(!rect||!img) return;
  var w=img.width-1, h=img.height-1;
  var nx1=Math.round(rect.x1/w*1000), ny1=Math.round(rect.y1/h*1000);
  var nx2=Math.round(rect.x2/w*1000), ny2=Math.round(rect.y2/h*1000);
  document.getElementById('rectInfo').textContent='归一化矩形: ('+nx1+','+ny1+') → ('+nx2+','+ny2+')';
  document.getElementById('rectInfo').className='status-line ok';
}

function drawImage(){
  if(!img) return;
  canvas.width=img.width;canvas.height=img.height;
  ctx.drawImage(img,0,0);
}
function drawWithRect(x1,y1,x2,y2){
  drawImage();
  ctx.strokeStyle='#2563eb';ctx.lineWidth=Math.max(2,canvas.width/300);
  ctx.strokeRect(Math.min(x1,x2),Math.min(y1,y2),Math.abs(x2-x1),Math.abs(y2-y1));
}

function loadImage(url){
  var i=new Image();
  i.onload=function(){
    img=i;rect=null;
    setupCanvas();drawImage();
    document.getElementById('canvasWrap').style.display='inline-block';
    document.getElementById('rectInfo').textContent='拖拽鼠标画矩形选中目标';
    document.getElementById('rectInfo').className='status-line muted';
  };
  i.src=url;
}

function grabFromDevice(){
  var btn=document.getElementById('grabBtn');btn.disabled=true;
  fetch('/api/screenshot.jpg?t='+Date.now(),{cache:'no-store'}).then(function(r){
    if(!r.ok) throw new Error('HTTP '+r.status);
    return r.blob();
  }).then(function(blob){
    loadImage(URL.createObjectURL(blob));
    btn.disabled=false;
  }).catch(function(e){
    alert('抓取失败: '+e.message);btn.disabled=false;
  });
}

document.getElementById('fileInput').addEventListener('change',function(e){
  var f=e.target.files[0];if(!f)return;
  var fr=new FileReader();
  fr.onload=function(ev){loadImage(ev.target.result)};
  fr.readAsDataURL(f);
});

document.addEventListener('keydown',function(e){
  if((e.ctrlKey||e.metaKey)&&e.key==='v'&&navigator.clipboard&&navigator.clipboard.read){
    navigator.clipboard.read().then(function(items){
      for(var i=0;i<items.length;i++){
        var it=items[i];
        for(var j=0;j<it.types.length;j++){
          var t=it.types[j];
          if(t.indexOf('image/')===0){
            it.getType(t).then(function(b){
              var fr=new FileReader();
              fr.onload=function(ev){loadImage(ev.target.result)};
              fr.readAsDataURL(b);
            });
            return;
          }
        }
      }
    }).catch(function(){});
  }
});

function canvasToB64(){
  return new Promise(function(resolve,reject){
    canvas.toBlob(function(b){
      var fr=new FileReader();
      fr.onloadend=function(){resolve(fr.result.split(',')[1])};
      fr.onerror=reject;
      fr.readAsDataURL(b);
    },'image/jpeg',0.85);
  });
}

function generateTask(){
  var msg=document.getElementById('msg');
  msg.textContent='生成中...';msg.className='status-line muted';
  var targetName=document.getElementById('targetName').value.trim();
  var userIntent=document.getElementById('userIntent').value.trim();
  if(!targetName||!userIntent){msg.textContent='请填写目标对象和 agent 任务描述';msg.className='status-line err';return}
  if(!rect){msg.textContent='请先在画面上画矩形';msg.className='status-line err';return}
  var taskId=generateTaskId(targetName);
  lastTaskId=taskId;
  var w=img.width-1, h=img.height-1;
  var box={
    x1:Math.round(rect.x1/w*1000),y1:Math.round(rect.y1/h*1000),
    x2:Math.round(rect.x2/w*1000),y2:Math.round(rect.y2/h*1000)
  };
  document.getElementById('genBtn').disabled=true;
  canvasToB64().then(function(b64){
    return fetch('/benchmark/suites/generate-perception',{
      method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({
        name:'perception_v1',task_id:taskId,user_intent:userIntent,
        screenshot_b64:b64,target_box_normalized:box,target_name:targetName
      })
    });
  }).then(function(r){return r.json()}).then(function(d){
    document.getElementById('genBtn').disabled=false;
    if(!d.ok){msg.textContent=d.error||'生成失败';msg.className='status-line err';return}
    lastTaskJSON=d.task_json;
    document.getElementById('jsonPreview').textContent=lastTaskJSON;
    document.getElementById('jsonPreview').contentEditable='true';
    document.getElementById('importBtn').disabled=false;
    msg.textContent='已生成（task_id: '+taskId+'），可编辑后追加';
    msg.className='status-line ok';
  }).catch(function(e){
    document.getElementById('genBtn').disabled=false;
    msg.textContent='生成失败: '+e;msg.className='status-line err';
  });
}

function importTask(){
  var msg=document.getElementById('msg');
  var taskRaw=document.getElementById('jsonPreview').textContent.trim();
  try{JSON.parse(taskRaw)}
  catch(e){msg.textContent='task JSON 解析失败: '+e;msg.className='status-line err';return}
  msg.textContent='追加中...';msg.className='status-line muted';
  document.getElementById('importBtn').disabled=true;
  canvasToB64().then(function(b64){
    return fetch('/benchmark/suites/append-perception',{
      method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({
        task_json:taskRaw,task_id:lastTaskId,screenshot_b64:b64
      })
    });
  }).then(function(r){return r.json()}).then(function(d){
    if(!d.ok){
      msg.textContent=d.error||'追加失败';msg.className='status-line err';
      document.getElementById('importBtn').disabled=false;
      return;
    }
    msg.innerHTML='✓ 已追加到 perception_v1（共 '+d.tasks_count+' 个 task）<br><span class="muted">仅写入设备本地。同步回 git 仓库可在根目录执行：<code>scripts/sync_perception_from_device.sh '+location.hostname+'</code></span>';msg.className='status-line ok';
    document.getElementById('importBtn').disabled=true;
  }).catch(function(e){
    msg.textContent='追加失败: '+e;msg.className='status-line err';
    document.getElementById('importBtn').disabled=false;
  });
}
</script></body></html>
`
