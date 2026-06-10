package agent

// benchmarkIndexHTML is the benchmark management page served at GET /benchmark.
// Migrated 1:1 from config_web.cpp benchmark_html_page() (lines 2520-2777).
const benchmarkIndexHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Aiden Benchmark</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;padding:24px;color:#333;max-width:1120px;margin:0 auto}
h1{font-size:20px;margin-bottom:14px}
.card{background:#fff;border-radius:8px;padding:16px;margin-bottom:14px;box-shadow:0 1px 3px rgba(0,0,0,.1)}
select,button{font-size:14px;padding:8px 16px;border-radius:6px;border:1px solid #ddd}
button{background:#2563eb;color:#fff;border:none;cursor:pointer}
button:disabled{background:#94a3b8;cursor:not-allowed}
button:hover:not(:disabled){background:#1d4ed8}
.status{font-size:13px;color:#475569;background:#f1f5f9;border-radius:999px;padding:5px 10px;display:inline-flex;align-items:center;min-height:28px}
.status.running{color:#92400e;background:#fef3c7}
.status.done{color:#16a34a}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:8px;border-bottom:1px solid #e5e7eb}
th{background:#f9fafb;font-weight:600;font-size:13px}
td{font-size:14px}
td a{color:#2563eb;text-decoration:none}
td a:hover{text-decoration:underline}
.pass{color:#16a34a}
.fail{color:#dc2626}
.progress{margin-top:8px;display:none}
.progress-bar{background:#e5e7eb;border-radius:999px;height:6px;overflow:hidden}
.progress-fill{background:#2563eb;height:100%;transition:width .3s ease}
#logBox{font-family:ui-monospace,monospace;font-size:12px;background:#18181b;color:#d4d4d8;padding:12px;border-radius:6px;max-height:280px;overflow-y:auto;white-space:pre-wrap;word-break:break-all;margin-top:12px}
.sec-head{font-size:15px;font-weight:600;margin-bottom:10px;display:flex;align-items:center;justify-content:space-between}
textarea{width:100%;font-family:ui-monospace,monospace;font-size:13px;padding:10px;border:1px solid #ddd;border-radius:6px;resize:vertical}
input[type=text]{font-size:14px;padding:8px 12px;border-radius:6px;border:1px solid #ddd;width:240px}
.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:8px}
.toolbar{margin-bottom:0}
.muted{font-size:12px;color:#888}
.err{color:#dc2626;font-size:13px;white-space:pre-wrap;margin-top:8px}
.ok{color:#16a34a;font-size:13px;margin-top:8px}
.del{background:#dc2626;font-size:12px;padding:4px 8px}
.del:hover:not(:disabled){background:#b91c1c}
</style></head><body>
<h1>Aiden Benchmark</h1>
<div class="card">
<div class="row toolbar">
<select id="suiteSelect"><option value="">Loading...</option></select>
<button id="runBtn" onclick="startRun()">Run</button>
<button id="delBtn" class="del" onclick="deleteSuite()" style="display:none">Delete</button>
<span id="statusText" class="status">idle</span>
</div>
<div id="progressBox" class="progress">
<div class="progress-bar"><div id="progressFill" class="progress-fill"></div></div>
</div>
</div>
<div class="card">
<div class="sec-head">Run history<button onclick="location.href='/benchmark/record'" style="font-size:13px;padding:6px 12px">📷 Record task</button></div>
<table><thead><tr><th>Run ID</th><th>Suite</th><th>Passed</th><th>Failed</th><th>Report</th></tr></thead>
<tbody id="historyBody"><tr><td colspan="5">Loading...</td></tr></tbody></table>
</div>
<div class="card">
<div class="sec-head">Import suite<button onclick="showImportForm()" id="importShowBtn" style="font-size:12px;padding:4px 10px">Show</button></div>
<div id="importForm" style="display:none">
<div class="row"><input type="text" id="importName" placeholder="Suite name (e.g. my_suite)"></div>
<textarea id="importJSON" rows="8" placeholder='Paste suite JSON here, e.g. {"name":"my_suite","tasks":[...]}'></textarea>
<div class="row"><button onclick="doImport()">Import</button><span id="importMsg"></span></div>
</div>
</div>
<div class="card">
<div class="sec-head">Generate suite with LLM</div>
<div class="row"><input type="text" id="genName" placeholder="Suite name"></div>
<textarea id="genPrompt" rows="6" placeholder="Describe what you want the benchmark to test. Example: Check if the agent can open WeChat, navigate to Moments, and post a status."></textarea>
<div class="row"><button onclick="generateSuite()">Generate</button><span id="genMsg"></span></div>
<div id="genPreview" style="display:none;margin-top:12px">
<div class="sec-head">Preview (edit if needed)</div>
<textarea id="genOutput" rows="16"></textarea>
<div class="row"><button onclick="importGenerated()">Import to suite</button><span id="genImportMsg"></span></div>
</div>
</div>
<div class="card">
<div class="sec-head">Benchmark log<button onclick="loadLog()" style="font-size:12px;padding:4px 10px">Refresh</button></div>
<div id="logBox">Loading...</div>
</div>
<script>
var suiteIndex={};
var pollTimer=null;
function showImportForm(){
document.getElementById('importForm').style.display='block';
document.getElementById('importShowBtn').style.display='none';
}
function startRun(){
var path=document.getElementById('suiteSelect').value;
if(!path){alert('Select a suite');return}
document.getElementById('runBtn').disabled=true;
fetch('/benchmark/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({suite:path})})
.then(r=>r.json()).then(function(d){
if(d.ok||d.status==='running'){
document.getElementById('statusText').textContent='starting...';
document.getElementById('statusText').className='status running';
startPolling();
}else{
alert('Failed to start: '+(d.error||'unknown'));
document.getElementById('runBtn').disabled=false;
}
}).catch(function(e){alert('Start failed: '+e);document.getElementById('runBtn').disabled=false});
}
function startPolling(){
if(pollTimer)clearInterval(pollTimer);
pollTimer=setInterval(function(){loadStatus();loadLog()},1500);
loadStatus();loadLog();
}
function stopPolling(){
if(pollTimer){clearInterval(pollTimer);pollTimer=null}
}
function updateStatus(d){
var status=document.getElementById('statusText');
var btn=document.getElementById('runBtn');
var box=document.getElementById('progressBox');
var fill=document.getElementById('progressFill');
status.textContent=d.status||'idle';
status.className='status '+(d.status||'');
if(d.status==='running'){
btn.disabled=true;
box.style.display='block';
var total=Number(d.total||0);
var done=Number(d.done||0);
var shown=Number(d.current||done||0);
fill.style.width=total?Math.max(0,Math.min(100,shown/total*100))+'%':'0';
}else{
box.style.display='none';
status.textContent=d.status||'idle';
status.className='status '+(d.status||'');
fill.style.width='0';
}}
function loadLog(){
fetch('/benchmark/log').then(r=>r.text()).then(function(t){
var box=document.getElementById('logBox');
var atBottom=(box.scrollTop+box.clientHeight>=box.scrollHeight-24);
box.textContent=t||'No benchmark log yet.';
if(atBottom)box.scrollTop=box.scrollHeight;
}).catch(function(){});
}
function load(){loadSuites();loadRuns();loadStatus()}
function loadSuites(){
fetch('/benchmark/suites').then(r=>r.json()).then(d=>{
var s=document.getElementById('suiteSelect');s.innerHTML='';suiteIndex={};
d.forEach(function(x){
var o=document.createElement('option');o.value=x.path;
o.textContent=x.name+(x.custom?' (custom)':'');
s.appendChild(o);suiteIndex[x.path]=x;
});
syncDelBtn();
})}
function syncDelBtn(){
var p=document.getElementById('suiteSelect').value;
var x=suiteIndex[p];
document.getElementById('delBtn').style.display=(x&&x.custom)?'inline-block':'none';
}
function loadRuns(){
fetch('/benchmark/runs').then(r=>r.json()).then(d=>{
var tb=document.getElementById('historyBody');tb.innerHTML='';
if(!d.length){tb.innerHTML='<tr><td colspan="5">No runs yet</td></tr>';return}
d.forEach(function(r){
var t=r.totals||{};var suite=r.suite||'';
var sn=suite.split('/').pop().replace('.json','');
var tr=document.createElement('tr');
tr.innerHTML='<td>'+r.run_id+'</td><td>'+sn+'</td>'
+'<td class="pass">'+(t.passed||0)+'</td><td class="fail">'+(t.failed||0)+'</td>'
+'<td><a href="/benchmark/report/'+r.run_id+'">View</a></td>';
tb.appendChild(tr)});
})}
function loadStatus(){
fetch('/benchmark/status').then(r=>r.json()).then(d=>{
var btn=document.getElementById('runBtn');
btn.disabled=(d.status==='running');
updateStatus(d);
if(d.status==='running'){
startPolling();
}else{
stopPolling();
if(d.recovered){
loadRuns();
}
}
}).catch(function(){});
}
document.getElementById('suiteSelect').addEventListener('change',syncDelBtn);
function deleteSuite(){
var p=document.getElementById('suiteSelect').value;
var x=suiteIndex[p];
if(!x||!x.custom){alert('Only custom suites can be deleted');return}
if(!confirm('Delete suite "'+x.name+'"?'))return;
fetch('/benchmark/suites/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:x.name})})
.then(r=>r.json()).then(function(d){
if(d.ok){loadSuites()}else{alert('Delete failed: '+(d.error||'unknown'))}
}).catch(function(e){alert('Delete failed: '+e)});
}
function doImport(){
var name=document.getElementById('importName').value.trim();
var json=document.getElementById('importJSON').value.trim();
var msg=document.getElementById('importMsg');
msg.textContent='';msg.className='';
if(!name||!json){msg.textContent='Name and JSON required';msg.className='err';return}
msg.textContent='Importing...';
fetch('/benchmark/suites/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,json:json})})
.then(r=>r.json()).then(function(d){
if(d.ok){
msg.textContent='Imported: '+d.path;msg.className='ok';
document.getElementById('importName').value='';
document.getElementById('importJSON').value='';
loadSuites();
}else{
msg.textContent='Import failed:\n'+(d.error||'unknown');msg.className='err';
}
}).catch(function(e){msg.textContent='Import failed: '+e;msg.className='err'});
}
function generateSuite(){
var name=document.getElementById('genName').value.trim();
var prompt=document.getElementById('genPrompt').value.trim();
var msg=document.getElementById('genMsg');
msg.textContent='';msg.className='';
if(!name||!prompt){msg.textContent='Name and prompt required';msg.className='err';return}
msg.textContent='Generating with LLM (may take 30s)...';
fetch('/benchmark/suites/generate',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,prompt:prompt})})
.then(r=>r.json()).then(function(d){
if(d.ok){
msg.textContent='Generated! Review below and click Import.';msg.className='ok';
document.getElementById('genOutput').value=d.suite_json;
document.getElementById('genPreview').style.display='block';
}else{
msg.textContent='Generation failed:\n'+(d.error||'unknown');msg.className='err';
}
}).catch(function(e){msg.textContent='Generation failed: '+e;msg.className='err'});
}
function importGenerated(){
var name=document.getElementById('genName').value.trim();
var json=document.getElementById('genOutput').value.trim();
var msg=document.getElementById('genImportMsg');
msg.textContent='';msg.className='';
if(!json){msg.textContent='No generated suite to import';msg.className='err';return}
msg.textContent='Importing...';
fetch('/benchmark/suites/import',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,json:json})})
.then(r=>r.json()).then(function(d){
if(d.ok){
msg.textContent='Imported: '+d.path;msg.className='ok';
document.getElementById('genName').value='';
document.getElementById('genPrompt').value='';
document.getElementById('genOutput').value='';
document.getElementById('genPreview').style.display='none';
document.getElementById('genMsg').textContent='';
loadSuites();
}else{
msg.textContent='Import failed:\n'+(d.error||'unknown');msg.className='err';
}
}).catch(function(e){msg.textContent='Import failed: '+e;msg.className='err'});
}
load();
setInterval(function(){if(!pollTimer)loadRuns()},10000);
</script></body></html>
`

// benchmarkRecordHTML is the screenshot-task recorder served at /benchmark/record.
// Reuses the image loading + normalized-coordinate logic from coordinate_debug_html.go;
// adds rectangle drag, target name, and a "Generate with LLM" button that POSTs to
// /benchmark/suites/generate-perception then to /benchmark/suites/import-with-assets.
const benchmarkRecordHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>录入截图任务</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f1ede2; color: #1e241d; padding: 20px; }
.container { max-width: 1100px; margin: 0 auto; background: #fffbf5; border-radius: 16px; padding: 24px; box-shadow: 0 8px 22px rgba(43,47,40,0.08); }
h1 { color: #155646; margin-bottom: 8px; font-size: 22px; }
.back-link { color: #1f7a63; font-size: 13px; text-decoration: none; }
input[type="text"], textarea { width: 100%; padding: 8px; border: 1px solid #d8cfbf; border-radius: 6px; font-size: 14px; background: #fffdf8; }
textarea { min-height: 60px; font-family: inherit; }
label { display: block; font-size: 12px; color: #43493d; margin: 12px 0 4px; font-weight: 600; }
.btn { padding: 10px 16px; background: #1f7a63; color: #fff; border: none; border-radius: 6px; cursor: pointer; font-size: 14px; }
.btn:hover { background: #155646; }
.btn:disabled { background: #b9c2b6; cursor: not-allowed; }
.btn-secondary { background: #be7d34; }
.btn-secondary:hover { background: #9d6328; }
.canvas-wrap { margin: 12px 0; border: 2px solid #1f7a63; border-radius: 8px; display: inline-block; max-width: 100%; }
canvas { display: block; cursor: crosshair; max-width: 100%; height: auto; }
.row { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
.muted { color: #697063; font-size: 12px; }
.field-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.json-preview { background: #1e241d; color: #cce4d8; padding: 12px; border-radius: 6px; font-family: monospace; font-size: 12px; white-space: pre-wrap; max-height: 320px; overflow: auto; }
.error { color: #be4334; font-size: 13px; margin-top: 6px; }
.ok { color: #1f7a63; font-size: 13px; margin-top: 6px; }
</style>
</head>
<body>
<div class="container">
<a class="back-link" href="/benchmark">← 返回 Benchmark</a>
<h1>📷 录入截图任务</h1>

<div class="field-grid">
<div>
<label for="suiteName">Suite name</label>
<input type="text" id="suiteName" placeholder="iphone_perception">
</div>
<div>
<label for="taskId">Task ID</label>
<input type="text" id="taskId" placeholder="find_settings_iphone">
</div>
</div>

<label>截图</label>
<div class="row">
<button class="btn" id="grabBtn">📷 抓取设备画面</button>
<input type="file" id="fileInput" accept="image/*" style="display:none">
<button class="btn btn-secondary" id="uploadBtn">📁 上传图片</button>
<span class="muted">或 Ctrl/Cmd+V 粘贴</span>
</div>
<div class="canvas-wrap" id="canvasWrap" style="display:none">
<canvas id="canvas"></canvas>
</div>
<div class="muted" id="rectInfo">在画面上拖拽鼠标画矩形选中目标</div>

<div class="field-grid">
<div>
<label for="targetName">Target name</label>
<input type="text" id="targetName" placeholder="Settings icon">
</div>
<div>
<label for="userIntent">User intent</label>
<input type="text" id="userIntent" placeholder="打开设置 app">
</div>
</div>

<div class="row" style="margin-top:14px">
<button class="btn" id="genBtn" disabled>✨ Generate with LLM</button>
<button class="btn btn-secondary" id="importBtn" disabled>Import to suite</button>
<span id="msg"></span>
</div>

<label>预览生成的 task JSON（可手动修改）</label>
<div class="json-preview" id="jsonPreview" contenteditable="false">（点 Generate 后显示）</div>
</div>

<script>
let img=null, rect=null, canvas, ctx, lastTaskJSON='';
const wrap=document.getElementById('canvasWrap');

function setupCanvas(){
  canvas=document.getElementById('canvas');
  ctx=canvas.getContext('2d');
  let dragging=false, startX=0, startY=0;
  canvas.addEventListener('mousedown',e=>{
    const r=canvas.getBoundingClientRect();
    startX=(e.clientX-r.left)*canvas.width/r.width;
    startY=(e.clientY-r.top)*canvas.height/r.height;
    dragging=true;
  });
  canvas.addEventListener('mousemove',e=>{
    if(!dragging) return;
    const r=canvas.getBoundingClientRect();
    const x=(e.clientX-r.left)*canvas.width/r.width;
    const y=(e.clientY-r.top)*canvas.height/r.height;
    drawWithRect(startX,startY,x,y);
  });
  canvas.addEventListener('mouseup',e=>{
    if(!dragging) return;
    dragging=false;
    const r=canvas.getBoundingClientRect();
    const x=(e.clientX-r.left)*canvas.width/r.width;
    const y=(e.clientY-r.top)*canvas.height/r.height;
    rect={x1:Math.min(startX,x),y1:Math.min(startY,y),x2:Math.max(startX,x),y2:Math.max(startY,y)};
    drawWithRect(rect.x1,rect.y1,rect.x2,rect.y2);
    showRectInfo();
    document.getElementById('genBtn').disabled=false;
  });
}

function showRectInfo(){
  if(!rect||!img){return}
  const w=img.width-1, h=img.height-1;
  const nx1=Math.round(rect.x1/w*1000), ny1=Math.round(rect.y1/h*1000);
  const nx2=Math.round(rect.x2/w*1000), ny2=Math.round(rect.y2/h*1000);
  document.getElementById('rectInfo').textContent='Normalized rectangle: ('+nx1+','+ny1+')-('+nx2+','+ny2+')';
}

function drawImage(){
  if(!img) return;
  canvas.width=img.width; canvas.height=img.height;
  ctx.drawImage(img,0,0);
}
function drawWithRect(x1,y1,x2,y2){
  drawImage();
  ctx.strokeStyle='#be4334'; ctx.lineWidth=4;
  ctx.strokeRect(Math.min(x1,x2),Math.min(y1,y2),Math.abs(x2-x1),Math.abs(y2-y1));
}

function loadImage(url, onDone){
  const i=new Image();
  i.onload=()=>{img=i; rect=null; setupCanvas(); drawImage(); wrap.style.display='inline-block'; if(onDone) onDone()};
  i.src=url;
}

document.getElementById('grabBtn').addEventListener('click',async()=>{
  const r=await fetch('/api/screenshot.jpg?t='+Date.now(),{cache:'no-store'});
  if(!r.ok){alert('抓取失败 '+r.status); return}
  const blob=await r.blob();
  loadImage(URL.createObjectURL(blob));
});
document.getElementById('uploadBtn').addEventListener('click',()=>document.getElementById('fileInput').click());
document.getElementById('fileInput').addEventListener('change',e=>{
  const f=e.target.files[0]; if(!f) return;
  const fr=new FileReader();
  fr.onload=ev=>loadImage(ev.target.result);
  fr.readAsDataURL(f);
});
document.addEventListener('keydown',e=>{
  if((e.ctrlKey||e.metaKey)&&e.key==='v'&&navigator.clipboard&&navigator.clipboard.read){
    navigator.clipboard.read().then(items=>{
      for(const it of items) for(const t of it.types) if(t.startsWith('image/')){
        it.getType(t).then(b=>{
          const fr=new FileReader();
          fr.onload=ev=>loadImage(ev.target.result);
          fr.readAsDataURL(b);
        });
        return;
      }
    }).catch(()=>{});
  }
});

async function blobOrCanvasToB64(){
  return new Promise((resolve,reject)=>{
    canvas.toBlob(b=>{
      const fr=new FileReader();
      fr.onloadend=()=>resolve(fr.result.split(',')[1]);
      fr.onerror=reject;
      fr.readAsDataURL(b);
    },'image/jpeg',0.85);
  });
}

document.getElementById('genBtn').addEventListener('click',async()=>{
  const msg=document.getElementById('msg');
  msg.textContent='生成中...'; msg.className='';
  try {
    const w=img.width-1, h=img.height-1;
    const box={
      x1:Math.round(rect.x1/w*1000), y1:Math.round(rect.y1/h*1000),
      x2:Math.round(rect.x2/w*1000), y2:Math.round(rect.y2/h*1000)
    };
    const b64=await blobOrCanvasToB64();
    const resp=await fetch('/benchmark/suites/generate-perception',{
      method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({
        name:document.getElementById('suiteName').value.trim(),
        task_id:document.getElementById('taskId').value.trim(),
        user_intent:document.getElementById('userIntent').value.trim(),
        screenshot_b64:b64,
        target_box_normalized:box,
        target_name:document.getElementById('targetName').value.trim()
      })
    });
    const d=await resp.json();
    if(!d.ok){msg.textContent=d.error||'生成失败'; msg.className='error'; return}
    lastTaskJSON=d.task_json;
    document.getElementById('jsonPreview').textContent=lastTaskJSON;
    document.getElementById('jsonPreview').contentEditable='true';
    document.getElementById('importBtn').disabled=false;
    msg.textContent='已生成，预览后点 Import';msg.className='ok';
  } catch(e) { msg.textContent=String(e); msg.className='error'; }
});

document.getElementById('importBtn').addEventListener('click',async()=>{
  const msg=document.getElementById('msg');
  const name=document.getElementById('suiteName').value.trim();
  const taskId=document.getElementById('taskId').value.trim();
  const taskRaw=document.getElementById('jsonPreview').textContent.trim();
  let task;
  try { task=JSON.parse(taskRaw).task; } catch(e) { msg.textContent='task JSON 解析失败: '+e; msg.className='error'; return }
  const suiteJSON=JSON.stringify({name:name, tasks:[task]});
  const b64=await blobOrCanvasToB64();
  msg.textContent='导入中...';msg.className='';
  const resp=await fetch('/benchmark/suites/import-with-assets',{
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({
      name:name, suite_json:suiteJSON,
      assets:[{task_id:taskId, screenshot_b64:b64}]
    })
  });
  const d=await resp.json();
  if(!d.ok){msg.textContent=d.error||'导入失败'; msg.className='error'; return}
  msg.textContent='导入成功 → '+d.path;msg.className='ok';
});

console.log('📷 录入截图任务已加载');
</script>
</body>
</html>
`
