package agent

// coordinateDebugHTML is the normalized-coordinate debug tool served at
// /coordinate-debug. It loads the live device screen via /api/screenshot.jpg
// (or a local upload) and maps clicks to the agent's 0-1000 normalized
// coordinate space used by the HID tools.
const coordinateDebugHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>归一化坐标调试工具</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f1ede2; color: #1e241d; padding: 20px; }
.container { max-width: 1400px; margin: 0 auto; background: rgba(255,251,245,0.92); border-radius: 20px; box-shadow: 0 12px 26px rgba(43,47,40,0.08); padding: 30px; }
h1 { color: #155646; margin-bottom: 8px; font-size: 26px; }
.back-link { display: inline-block; margin-bottom: 20px; color: #1f7a63; text-decoration: none; font-size: 14px; }
.back-link:hover { text-decoration: underline; }
.controls { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; margin-bottom: 24px; }
.control-section { background: #efe7da; padding: 20px; border-radius: 14px; }
h2 { font-size: 17px; color: #43493d; margin-bottom: 14px; }
.upload-area { border: 2px dashed #c9bfae; border-radius: 12px; padding: 26px; text-align: center; cursor: pointer; transition: all 0.2s; margin-bottom: 14px; }
.upload-area:hover { border-color: #1f7a63; background: #e7f0ea; }
.upload-area.drag-over { border-color: #1f7a63; background: #dcebe2; }
input[type="file"] { display: none; }
.input-group { margin-bottom: 14px; }
label { display: block; font-size: 13px; color: #697063; margin-bottom: 5px; }
input[type="number"] { width: 100%; padding: 10px; border: 1px solid #d8cfbf; border-radius: 8px; font-size: 14px; background: #fffdf8; color: #1e241d; }
.btn { width: 100%; padding: 12px; background: #1f7a63; color: #fff; border: none; border-radius: 8px; font-size: 15px; cursor: pointer; transition: background 0.2s; margin-bottom: 10px; }
.btn:hover { background: #155646; }
.btn:disabled { background: #b9c2b6; cursor: not-allowed; }
.btn-clear { background: #be4334; }
.btn-clear:hover { background: #9d362a; }
.checkbox-row { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; font-size: 13px; color: #43493d; }
.status-text { font-size: 13px; color: #697063; margin-top: 6px; min-height: 18px; }
.canvas-container { position: relative; display: inline-block; margin: 16px auto; max-width: 100%; }
canvas { border: 2px solid #1f7a63; border-radius: 10px; cursor: crosshair; max-width: 100%; height: auto; display: block; }
.info-panel { background: #e7f0ea; padding: 16px; border-radius: 12px; margin-top: 16px; }
.info-item { display: flex; justify-content: space-between; margin-bottom: 8px; font-size: 14px; }
.info-label { font-weight: 600; color: #155646; }
.info-value { font-family: "Courier New", monospace; color: #1e241d; }
</style>
</head>
<body>
<div class="container">
<h1>🎯 归一化坐标调试工具</h1>
<a class="back-link" href="/">← 返回 Aiden Agent</a>

<div class="controls">
<div class="control-section">
<h2>📤 加载截图</h2>
<button class="btn" id="loadDeviceBtn">📷 从设备抓取当前画面</button>
<div class="checkbox-row"><input type="checkbox" id="autoRefresh"><label for="autoRefresh" style="margin:0;">自动刷新 (2s)</label></div>
<div class="status-text" id="deviceStatus"></div>
<div class="upload-area" id="uploadArea">
<p>📁 点击或拖拽图片到此处</p>
<p style="font-size:12px;color:#999;margin-top:8px;">也可 Ctrl/Cmd+V 粘贴图片</p>
</div>
<input type="file" id="fileInput" accept="image/*">
</div>

<div class="control-section">
<h2>📍 输入归一化坐标 (0-1000)</h2>
<div class="input-group">
<label for="coordX">X 坐标</label>
<input type="number" id="coordX" min="0" max="1000" placeholder="0 - 1000">
</div>
<div class="input-group">
<label for="coordY">Y 坐标</label>
<input type="number" id="coordY" min="0" max="1000" placeholder="0 - 1000">
</div>
<button class="btn" id="showCoordBtn" disabled>显示坐标位置</button>
<button class="btn btn-clear" id="clearBtn" disabled>清除标记</button>
</div>
</div>

<div id="canvasWrapper" style="text-align:center;display:none;">
<div class="canvas-container"><canvas id="canvas"></canvas></div>
</div>

<div class="info-panel" id="infoPanel" style="display:none;">
<h2>📊 当前信息</h2>
<div class="info-item"><span class="info-label">图片尺寸:</span><span class="info-value" id="imageSize">-</span></div>
<div class="info-item"><span class="info-label">点击位置 (像素):</span><span class="info-value" id="clickPixel">-</span></div>
<div class="info-item"><span class="info-label">归一化坐标:</span><span class="info-value" id="normalizedCoord">-</span></div>
<div class="info-item"><span class="info-label">标记位置 (像素):</span><span class="info-value" id="markerPixel">-</span></div>
</div>
</div>
<script>
let currentImage = null;
let autoTimer = null;
let captureInFlight = false;

const uploadArea = document.getElementById('uploadArea');
const fileInput = document.getElementById('fileInput');
const canvas = document.getElementById('canvas');
const ctx = canvas.getContext('2d');
const canvasWrapper = document.getElementById('canvasWrapper');
const infoPanel = document.getElementById('infoPanel');
const coordX = document.getElementById('coordX');
const coordY = document.getElementById('coordY');
const showCoordBtn = document.getElementById('showCoordBtn');
const clearBtn = document.getElementById('clearBtn');
const loadDeviceBtn = document.getElementById('loadDeviceBtn');
const autoRefresh = document.getElementById('autoRefresh');
const deviceStatus = document.getElementById('deviceStatus');

uploadArea.addEventListener('click', () => fileInput.click());
uploadArea.addEventListener('dragover', (e) => { e.preventDefault(); uploadArea.classList.add('drag-over'); });
uploadArea.addEventListener('dragleave', () => uploadArea.classList.remove('drag-over'));
uploadArea.addEventListener('drop', (e) => {
    e.preventDefault();
    uploadArea.classList.remove('drag-over');
    if (e.dataTransfer.files.length > 0) handleFile(e.dataTransfer.files[0]);
});
fileInput.addEventListener('change', (e) => {
    if (e.target.files.length > 0) handleFile(e.target.files[0]);
});

async function captureFromDevice() {
    if (captureInFlight) return;
    captureInFlight = true;
    loadDeviceBtn.disabled = true;
    deviceStatus.textContent = '抓取中...';
    try {
        const res = await fetch('/api/screenshot.jpg?t=' + Date.now(), { cache: 'no-store' });
        if (!res.ok) {
            deviceStatus.textContent = '抓取失败: ' + res.status + ' ' + (await res.text());
            return;
        }
        const blob = await res.blob();
        const url = URL.createObjectURL(blob);
        loadImageFromUrl(url, '设备画面已加载', () => URL.revokeObjectURL(url));
    } catch (e) {
        deviceStatus.textContent = '请求失败: ' + e.message;
    } finally {
        captureInFlight = false;
        loadDeviceBtn.disabled = false;
    }
}

loadDeviceBtn.addEventListener('click', captureFromDevice);
autoRefresh.addEventListener('change', () => {
    if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
    if (autoRefresh.checked) autoTimer = setInterval(captureFromDevice, 2000);
});

function handleFile(file) {
    if (!file.type.startsWith('image/')) { alert('请选择图片文件！'); return; }
    const reader = new FileReader();
    reader.onload = (e) => loadImageFromUrl(e.target.result, '本地图片已加载');
    reader.readAsDataURL(file);
}

function loadImageFromUrl(url, statusText, onDone) {
    const img = new Image();
    img.onload = () => {
        currentImage = img;
        drawImage();
        canvasWrapper.style.display = 'block';
        infoPanel.style.display = 'block';
        showCoordBtn.disabled = false;
        clearBtn.disabled = false;
        document.getElementById('imageSize').textContent = img.width + ' × ' + img.height + ' px';
        deviceStatus.textContent = statusText;
        if (onDone) onDone();
    };
    img.onerror = () => { deviceStatus.textContent = '图片加载失败'; if (onDone) onDone(); };
    img.src = url;
}

function drawImage() {
    if (!currentImage) return;
    canvas.width = currentImage.width;
    canvas.height = currentImage.height;
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(currentImage, 0, 0);
}

canvas.addEventListener('click', (e) => {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    const pixelX = Math.round((e.clientX - rect.left) * scaleX);
    const pixelY = Math.round((e.clientY - rect.top) * scaleY);
    const normalizedX = Math.round((pixelX / canvas.width) * 1000);
    const normalizedY = Math.round((pixelY / canvas.height) * 1000);
    document.getElementById('clickPixel').textContent = 'X: ' + pixelX + ', Y: ' + pixelY;
    document.getElementById('normalizedCoord').textContent = 'X: ' + normalizedX + ', Y: ' + normalizedY;
    coordX.value = normalizedX;
    coordY.value = normalizedY;
    showMarker(pixelX, pixelY);
});

showCoordBtn.addEventListener('click', () => {
    const x = parseInt(coordX.value);
    const y = parseInt(coordY.value);
    if (isNaN(x) || isNaN(y) || x < 0 || x > 1000 || y < 0 || y > 1000) {
        alert('请输入有效的坐标值 (0-1000)！');
        return;
    }
    const pixelX = Math.round((x / 1000) * canvas.width);
    const pixelY = Math.round((y / 1000) * canvas.height);
    showMarker(pixelX, pixelY);
    document.getElementById('normalizedCoord').textContent = 'X: ' + x + ', Y: ' + y;
});

clearBtn.addEventListener('click', () => {
    drawImage();
    document.getElementById('clickPixel').textContent = '-';
    document.getElementById('normalizedCoord').textContent = '-';
    document.getElementById('markerPixel').textContent = '-';
    coordX.value = '';
    coordY.value = '';
});

function showMarker(pixelX, pixelY) {
    drawImage();
    ctx.strokeStyle = '#be4334';
    ctx.lineWidth = 3;
    ctx.beginPath();
    ctx.moveTo(pixelX - 15, pixelY); ctx.lineTo(pixelX + 15, pixelY);
    ctx.moveTo(pixelX, pixelY - 15); ctx.lineTo(pixelX, pixelY + 15);
    ctx.stroke();
    ctx.beginPath(); ctx.arc(pixelX, pixelY, 10, 0, 2 * Math.PI); ctx.stroke();
    ctx.fillStyle = '#be4334';
    ctx.beginPath(); ctx.arc(pixelX, pixelY, 3, 0, 2 * Math.PI); ctx.fill();
    document.getElementById('markerPixel').textContent = 'X: ' + pixelX + ', Y: ' + pixelY;
}

document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'v') {
        navigator.clipboard.read().then(items => {
            for (const item of items) {
                for (const type of item.types) {
                    if (type.startsWith('image/')) {
                        item.getType(type).then(blob => handleFile(blob));
                        return;
                    }
                }
            }
        }).catch(() => {});
    }
    if (e.key === 'Escape' && !clearBtn.disabled) clearBtn.click();
});

console.log('🎯 归一化坐标调试工具已加载');
</script>
</body>
</html>
`
