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
input[type="number"], select { width: 100%; padding: 10px; border: 1px solid #d8cfbf; border-radius: 8px; font-size: 14px; background: #fffdf8; color: #1e241d; }
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
.script-section { background: #efe7da; padding: 20px; border-radius: 14px; margin-top: 24px; }
.script-editor { width: 100%; min-height: 300px; padding: 12px; border: 1px solid #d8cfbf; border-radius: 8px; font-family: "Courier New", monospace; font-size: 13px; background: #fffdf8; color: #1e241d; resize: vertical; }
.script-result { margin-top: 16px; padding: 12px; background: #e7f0ea; border-radius: 8px; max-height: 400px; overflow-y: auto; }
.script-result pre { margin: 0; font-family: "Courier New", monospace; font-size: 12px; white-space: pre-wrap; word-wrap: break-word; }
.script-step { padding: 8px; margin-bottom: 8px; border-left: 3px solid #1f7a63; background: #fff; border-radius: 4px; }
.script-step.error { border-left-color: #be4334; background: #fef5f4; }
.script-step-header { font-weight: 600; margin-bottom: 4px; font-size: 13px; }
.script-step-output { font-size: 12px; color: #697063; margin-top: 4px; }
.btn-group { display: flex; gap: 10px; margin-bottom: 10px; }
.btn-group .btn { flex: 1; }
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
<div class="checkbox-row"><input type="checkbox" id="cropBlackBars" checked><label for="cropBlackBars" style="margin:0;">裁剪黑边</label></div>
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
<div class="input-group">
<label for="tapType">点击类型</label>
<select id="tapType">
<option value="tap">tap</option>
<option value="double_tap">double_tap</option>
<option value="long_press">long_press</option>
</select>
</div>
<button class="btn" id="showCoordBtn" disabled>显示坐标位置</button>
<button class="btn" id="tapCoordBtn" disabled>触发点击</button>
<button class="btn btn-clear" id="clearBtn" disabled>清除标记</button>
<div class="status-text" id="tapStatus"></div>
</div>
</div>

<div id="canvasWrapper" style="text-align:center;display:none;">
<div class="canvas-container"><canvas id="canvas"></canvas></div>
</div>

<div class="info-panel" id="infoPanel" style="display:none;">
<h2>📊 当前信息</h2>
<div class="info-item"><span class="info-label">图片尺寸:</span><span class="info-value" id="imageSize">-</span></div>
<div class="info-item"><span class="info-label">原始屏幕尺寸:</span><span class="info-value" id="originalScreenSize">-</span></div>
<div class="info-item"><span class="info-label">点击位置 (像素):</span><span class="info-value" id="clickPixel">-</span></div>
<div class="info-item"><span class="info-label">归一化坐标:</span><span class="info-value" id="normalizedCoord">-</span></div>
<div class="info-item"><span class="info-label">标记位置 (像素):</span><span class="info-value" id="markerPixel">-</span></div>
</div>

<div class="script-section">
<h2>📝 脚本编辑器</h2>
<p style="font-size:13px;color:#697063;margin-bottom:12px;">编辑 JSONL 格式的脚本，每行一个 JSON 对象。支持 wait、tts、call 三种指令。</p>
<textarea class="script-editor" id="scriptEditor" placeholder='{"tts":"开始演示"}
{"wait":500}
{"call":{"tool":"screenshot","input":{}}}
{"tts":"点击屏幕"}
{"call":{"tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}}
{"wait":1000}
{"call":{"tool":"screenshot","input":{}}}'></textarea>
<div class="btn-group">
<button class="btn" id="runScriptBtn">▶️ 运行脚本</button>
<button class="btn btn-clear" id="clearScriptBtn">🗑️ 清空编辑器</button>
</div>
<div class="status-text" id="scriptStatus"></div>
<div class="script-result" id="scriptResult" style="display:none;"></div>
</div>
</div>
<script>
let currentImage = null;
let currentScreenshotMeta = null;
let autoTimer = null;
let captureInFlight = false;
let tapInFlight = false;

const uploadArea = document.getElementById('uploadArea');
const fileInput = document.getElementById('fileInput');
const canvas = document.getElementById('canvas');
const ctx = canvas.getContext('2d');
const canvasWrapper = document.getElementById('canvasWrapper');
const infoPanel = document.getElementById('infoPanel');
const coordX = document.getElementById('coordX');
const coordY = document.getElementById('coordY');
const tapType = document.getElementById('tapType');
const showCoordBtn = document.getElementById('showCoordBtn');
const tapCoordBtn = document.getElementById('tapCoordBtn');
const clearBtn = document.getElementById('clearBtn');
const loadDeviceBtn = document.getElementById('loadDeviceBtn');
const cropBlackBars = document.getElementById('cropBlackBars');
const autoRefresh = document.getElementById('autoRefresh');
const deviceStatus = document.getElementById('deviceStatus');
const tapStatus = document.getElementById('tapStatus');
const scriptEditor = document.getElementById('scriptEditor');
const runScriptBtn = document.getElementById('runScriptBtn');
const clearScriptBtn = document.getElementById('clearScriptBtn');
const scriptStatus = document.getElementById('scriptStatus');
const scriptResult = document.getElementById('scriptResult');

let scriptInFlight = false;

function clamp(value, min, max) {
    return Math.min(Math.max(value, min), max);
}

function setCoordinateActionsEnabled(enabled) {
    showCoordBtn.disabled = !enabled;
    tapCoordBtn.disabled = !enabled || tapInFlight;
    clearBtn.disabled = !enabled;
}

function currentCropBlackBars() {
    return !!cropBlackBars.checked;
}

function defaultScreenshotMeta(width, height) {
    return {
        width,
        height,
        source_width: width,
        source_height: height,
        source_active_area: null,
        original_screen_width_pixels: null,
        original_screen_height_pixels: null
    };
}

function formatOriginalScreenSize(meta) {
    if (!meta) return '-';
    const width = meta.original_screen_width_pixels;
    const height = meta.original_screen_height_pixels;
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) return '-';
    return width + ' × ' + height + ' px';
}

function normalizeActiveArea(meta) {
    if (!meta || !meta.source_active_area || !meta.source_active_area.valid) return null;
    return meta.source_active_area;
}

function imageIsCropped(meta) {
    if (!meta) return false;
    return (meta.width || 0) !== (meta.source_width || meta.width || 0) ||
           (meta.height || 0) !== (meta.source_height || meta.height || 0);
}

function imagePixelToSourcePixel(pixelX, pixelY) {
    const meta = currentScreenshotMeta || defaultScreenshotMeta(canvas.width, canvas.height);
    const active = normalizeActiveArea(meta);
    if (imageIsCropped(meta) && active) {
        return {
            pixelX: clamp(active.x + pixelX, 0, Math.max((meta.source_width || canvas.width) - 1, 0)),
            pixelY: clamp(active.y + pixelY, 0, Math.max((meta.source_height || canvas.height) - 1, 0))
        };
    }
    return {
        pixelX: clamp(pixelX, 0, Math.max((meta.source_width || canvas.width) - 1, 0)),
        pixelY: clamp(pixelY, 0, Math.max((meta.source_height || canvas.height) - 1, 0))
    };
}

function sourcePixelToNormalized(pixelX, pixelY) {
    const meta = currentScreenshotMeta || defaultScreenshotMeta(canvas.width, canvas.height);
    const active = normalizeActiveArea(meta);
    // Normalized coordinates are relative to active_area (0-1000 maps to the mirrored phone touch region)
    if (active) {
        const activeWidth = Math.max(active.width, 1);
        const activeHeight = Math.max(active.height, 1);
        const activePixelX = clamp(pixelX - active.x, 0, active.width - 1);
        const activePixelY = clamp(pixelY - active.y, 0, active.height - 1);
        return {
            x: clamp(Math.round((activePixelX / Math.max(activeWidth - 1, 1)) * 1000), 0, 1000),
            y: clamp(Math.round((activePixelY / Math.max(activeHeight - 1, 1)) * 1000), 0, 1000)
        };
    }
    // Fallback: no active_area, treat as full frame
    const sourceWidth = Math.max(meta.source_width || canvas.width, 1);
    const sourceHeight = Math.max(meta.source_height || canvas.height, 1);
    const maxPixelX = Math.max(sourceWidth - 1, 1);
    const maxPixelY = Math.max(sourceHeight - 1, 1);
    return {
        x: clamp(Math.round((pixelX / maxPixelX) * 1000), 0, 1000),
        y: clamp(Math.round((pixelY / maxPixelY) * 1000), 0, 1000)
    };
}

function normalizedToSourcePixel(x, y) {
    const meta = currentScreenshotMeta || defaultScreenshotMeta(canvas.width, canvas.height);
    const active = normalizeActiveArea(meta);
    // Normalized coordinates are relative to active_area (0-1000 maps to the mirrored phone touch region)
    if (active) {
        const activeWidth = Math.max(active.width, 1);
        const activeHeight = Math.max(active.height, 1);
        const activePixelX = clamp(Math.round((x / 1000) * Math.max(activeWidth - 1, 0)), 0, activeWidth - 1);
        const activePixelY = clamp(Math.round((y / 1000) * Math.max(activeHeight - 1, 0)), 0, activeHeight - 1);
        return {
            pixelX: clamp(activePixelX + active.x, 0, (meta.source_width || canvas.width) - 1),
            pixelY: clamp(activePixelY + active.y, 0, (meta.source_height || canvas.height) - 1)
        };
    }
    // Fallback: no active_area, treat as full frame
    const sourceWidth = Math.max(meta.source_width || canvas.width, 1);
    const sourceHeight = Math.max(meta.source_height || canvas.height, 1);
    return {
        pixelX: clamp(Math.round((x / 1000) * Math.max(sourceWidth - 1, 0)), 0, sourceWidth - 1),
        pixelY: clamp(Math.round((y / 1000) * Math.max(sourceHeight - 1, 0)), 0, sourceHeight - 1)
    };
}

function sourcePixelToImagePixel(pixelX, pixelY) {
    const meta = currentScreenshotMeta || defaultScreenshotMeta(canvas.width, canvas.height);
    const active = normalizeActiveArea(meta);
    if (imageIsCropped(meta) && active) {
        return {
            pixelX: clamp(pixelX - active.x, 0, canvas.width - 1),
            pixelY: clamp(pixelY - active.y, 0, canvas.height - 1)
        };
    }
    return {
        pixelX: clamp(pixelX, 0, canvas.width - 1),
        pixelY: clamp(pixelY, 0, canvas.height - 1)
    };
}

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
        const query = new URLSearchParams({
            t: String(Date.now()),
            crop_black_bars: currentCropBlackBars() ? 'true' : 'false'
        });
        const res = await fetch('/api/screenshot.jpg?' + query.toString(), { cache: 'no-store' });
        if (!res.ok) {
            deviceStatus.textContent = '抓取失败: ' + res.status + ' ' + (await res.text());
            return;
        }
        const meta = {
            width: parseInt(res.headers.get('X-Frame-Width') || '0', 10) || 0,
            height: parseInt(res.headers.get('X-Frame-Height') || '0', 10) || 0,
            source_width: parseInt(res.headers.get('X-Source-Width') || '0', 10) || 0,
            source_height: parseInt(res.headers.get('X-Source-Height') || '0', 10) || 0,
            original_screen_width_pixels: res.headers.get('X-Original-Screen-Valid') === 'true' ? (parseInt(res.headers.get('X-Original-Screen-Width') || '0', 10) || 0) : null,
            original_screen_height_pixels: res.headers.get('X-Original-Screen-Valid') === 'true' ? (parseInt(res.headers.get('X-Original-Screen-Height') || '0', 10) || 0) : null,
            source_active_area: res.headers.get('X-Source-Active-Valid') === 'true' ? {
                x: parseInt(res.headers.get('X-Source-Active-X') || '0', 10) || 0,
                y: parseInt(res.headers.get('X-Source-Active-Y') || '0', 10) || 0,
                width: parseInt(res.headers.get('X-Source-Active-Width') || '0', 10) || 0,
                height: parseInt(res.headers.get('X-Source-Active-Height') || '0', 10) || 0,
                valid: true
            } : null
        };
        const blob = await res.blob();
        const url = URL.createObjectURL(blob);
        try {
            await loadImageFromUrl(url, '设备画面已加载', meta);
        } finally {
            URL.revokeObjectURL(url);
        }
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
    reader.onload = async (e) => {
        try {
            await loadImageFromUrl(e.target.result, '本地图片已加载');
        } catch (_) {}
    };
    reader.readAsDataURL(file);
}

function loadImageFromUrl(url, statusText, meta) {
    return new Promise((resolve, reject) => {
        const img = new Image();
        img.onload = () => {
            currentImage = img;
            currentScreenshotMeta = meta || defaultScreenshotMeta(img.width, img.height);
            drawImage();
            canvasWrapper.style.display = 'block';
            infoPanel.style.display = 'block';
            setCoordinateActionsEnabled(true);
            document.getElementById('imageSize').textContent = img.width + ' × ' + img.height + ' px';
            document.getElementById('originalScreenSize').textContent = formatOriginalScreenSize(currentScreenshotMeta);
            deviceStatus.textContent = statusText;
            resolve(img);
        };
        img.onerror = () => {
            deviceStatus.textContent = '图片加载失败';
            reject(new Error('图片加载失败'));
        };
        img.src = url;
    });
}

function loadImageFromScreenshotResult(result, statusText) {
    const format = result && result.format ? result.format : 'jpeg';
    return loadImageFromUrl('data:image/' + format + ';base64,' + result.data, statusText, result);
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
    const maxPixelX = canvas.width - 1;
    const maxPixelY = canvas.height - 1;
    const pixelX = clamp(Math.round((e.clientX - rect.left) * scaleX), 0, maxPixelX);
    const pixelY = clamp(Math.round((e.clientY - rect.top) * scaleY), 0, maxPixelY);
    const sourcePixel = imagePixelToSourcePixel(pixelX, pixelY);
    const normalized = sourcePixelToNormalized(sourcePixel.pixelX, sourcePixel.pixelY);
    document.getElementById('clickPixel').textContent = 'X: ' + pixelX + ', Y: ' + pixelY;
    document.getElementById('normalizedCoord').textContent = 'X: ' + normalized.x + ', Y: ' + normalized.y;
    coordX.value = normalized.x;
    coordY.value = normalized.y;
    showMarker(pixelX, pixelY);
});

function getNormalizedInputs() {
    const x = parseInt(coordX.value);
    const y = parseInt(coordY.value);
    if (isNaN(x) || isNaN(y) || x < 0 || x > 1000 || y < 0 || y > 1000) {
        return null;
    }
    return { x, y };
}

function normalizedToPixel(x, y) {
    const sourcePixel = normalizedToSourcePixel(x, y);
    return sourcePixelToImagePixel(sourcePixel.pixelX, sourcePixel.pixelY);
}

function showNormalizedMarker(x, y) {
    const pixel = normalizedToPixel(x, y);
    showMarker(pixel.pixelX, pixel.pixelY);
    document.getElementById('normalizedCoord').textContent = 'X: ' + x + ', Y: ' + y;
}

showCoordBtn.addEventListener('click', () => {
    const point = getNormalizedInputs();
    if (!point) {
        alert('请输入有效的坐标值 (0-1000)！');
        return;
    }
    showNormalizedMarker(point.x, point.y);
});

tapCoordBtn.addEventListener('click', async () => {
    const point = getNormalizedInputs();
    if (!point) {
        alert('请输入有效的坐标值 (0-1000)！');
        return;
    }
    tapInFlight = true;
    setCoordinateActionsEnabled(true);
    tapStatus.textContent = '点击执行中...';
    try {
        showNormalizedMarker(point.x, point.y);
        const res = await fetch('/api/coordinate-debug/tap', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                x: point.x,
                y: point.y,
                type: tapType.value,
                crop_black_bars: currentCropBlackBars()
            })
        });
        const bodyText = await res.text();
        let payload = null;
        try {
            payload = JSON.parse(bodyText);
        } catch (_) {}
        if (!res.ok) {
            tapStatus.textContent = payload && payload.error ? '点击失败: ' + payload.error : '点击失败: ' + bodyText;
            return;
        }
        if (!payload || !payload.ok || !payload.screenshot || !payload.screenshot.data) {
            tapStatus.textContent = '点击失败: 返回结果不完整';
            return;
        }
        await loadImageFromScreenshotResult(payload.screenshot, '点击后画面已刷新');
        showNormalizedMarker(point.x, point.y);
        tapStatus.textContent = '已触发' + tapType.options[tapType.selectedIndex].text + '，画面已刷新';
    } catch (e) {
        tapStatus.textContent = '点击失败: ' + e.message;
    } finally {
        tapInFlight = false;
        setCoordinateActionsEnabled(!!currentImage);
    }
});

clearBtn.addEventListener('click', () => {
    drawImage();
    document.getElementById('clickPixel').textContent = '-';
    document.getElementById('normalizedCoord').textContent = '-';
    document.getElementById('markerPixel').textContent = '-';
    coordX.value = '';
    coordY.value = '';
    tapStatus.textContent = '';
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
        if (!navigator.clipboard || typeof navigator.clipboard.read !== 'function') return;
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

// Script execution functionality
runScriptBtn.addEventListener('click', async () => {
    const content = scriptEditor.value.trim();
    if (!content) {
        scriptStatus.textContent = '脚本内容为空';
        return;
    }

    if (scriptInFlight) return;
    scriptInFlight = true;
    runScriptBtn.disabled = true;
    scriptStatus.textContent = '执行中...';
    scriptResult.style.display = 'none';

    try {
        // Execute script directly via inline execution
        const result = await executeScriptContent(content);
        displayScriptResult(result);
        scriptStatus.textContent = result.ok ? '✅ 脚本执行成功' : '❌ 脚本执行失败';
    } catch (e) {
        scriptStatus.textContent = '❌ 执行失败: ' + e.message;
        scriptResult.style.display = 'block';
        scriptResult.innerHTML = '<pre style="color:#be4334;">' + e.message + '</pre>';
    } finally {
        scriptInFlight = false;
        runScriptBtn.disabled = false;
    }
});

clearScriptBtn.addEventListener('click', () => {
    scriptEditor.value = '';
    scriptStatus.textContent = '';
    scriptResult.style.display = 'none';
});

async function executeScriptContent(content) {
    const lines = content.split('\n').filter(line => line.trim());
    const result = {
        ok: true,
        lines_read: lines.length,
        steps_run: 0,
        steps: [],
        error: ''
    };

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i].trim();
        if (!line) continue;

        const lineNo = i + 1;
        let step;
        try {
            step = JSON.parse(line);
        } catch (e) {
            result.ok = false;
            result.error = 'Line ' + lineNo + ': 无效的 JSON';
            result.steps.push({
                line: lineNo,
                ok: false,
                error: 'JSON 解析失败: ' + e.message
            });
            break;
        }

        const stepResult = await executeScriptStep(step, lineNo);
        result.steps.push(stepResult);
        result.steps_run++;

        if (!stepResult.ok) {
            result.ok = false;
            result.error = stepResult.error;
            break;
        }
    }

    return result;
}

async function executeScriptStep(step, lineNo) {
    const startTime = Date.now();
    const stepResult = {
        line: lineNo,
        ok: true,
        duration_ms: 0
    };

    // Parse step type (support both explicit and shorthand formats)
    let type = step.type;
    if (!type) {
        if ('wait' in step) type = 'wait';
        else if ('tts' in step) type = 'tts';
        else if ('call' in step) type = 'call';
    }

    stepResult.type = type;

    try {
        switch (type) {
            case 'wait':
                const ms = step.ms || step.wait_ms || step.duration_ms || step.wait || 0;
                if (ms <= 0) {
                    stepResult.ok = false;
                    stepResult.error = 'wait 时长必须大于 0';
                    break;
                }
                await new Promise(resolve => setTimeout(resolve, ms));
                stepResult.output = 'waited ' + ms + 'ms';
                break;

            case 'tts':
                const text = step.text || step.tts || '';
                if (!text.trim()) {
                    stepResult.ok = false;
                    stepResult.error = 'tts 文本为空';
                    break;
                }
                // TTS is async and non-blocking in the real tool, so we just acknowledge it
                stepResult.text = text;
                stepResult.output = 'queued';
                break;

            case 'call':
                const toolName = step.tool;
                let toolInput = step.input;

                // Handle shorthand call format: {"call":{"tool":"...","input":{...}}}
                if (!toolName && step.call) {
                    const callObj = step.call;
                    stepResult.tool = callObj.tool;
                    toolInput = callObj.input;
                } else {
                    stepResult.tool = toolName;
                }

                if (!stepResult.tool) {
                    stepResult.ok = false;
                    stepResult.error = 'call 缺少 tool 参数';
                    break;
                }

                // Execute tool via HTTP API
                const toolResponse = await fetch('/api/tools/' + stepResult.tool, {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(toolInput || {})
                });

                if (!toolResponse.ok) {
                    stepResult.ok = false;
                    stepResult.error = '工具调用失败: ' + toolResponse.statusText;
                    break;
                }

                const toolResult = await toolResponse.json();
                stepResult.output = toolResult.output;

                if (toolResult.is_error || toolResult.error) {
                    stepResult.ok = false;
                    stepResult.error = toolResult.error || toolResult.output;
                }

                // If the tool is screenshot, refresh the display
                if (stepResult.tool === 'screenshot' && toolResult.output && !stepResult.ok === false) {
                    try {
                        const screenshotData = JSON.parse(toolResult.output);
                        if (screenshotData.data) {
                            await loadImageFromScreenshotResult(screenshotData, '脚本截图');
                        }
                    } catch (e) {
                        // Ignore screenshot display errors
                    }
                }
                break;

            default:
                stepResult.ok = false;
                stepResult.error = '不支持的步骤类型: ' + type;
        }
    } catch (e) {
        stepResult.ok = false;
        stepResult.error = e.message;
    }

    stepResult.duration_ms = Date.now() - startTime;
    return stepResult;
}

function displayScriptResult(result) {
    scriptResult.style.display = 'block';

    let html = '<div style="margin-bottom:12px;font-weight:600;color:' + (result.ok ? '#155646' : '#be4334') + ';">';
    html += result.ok ? '✅ 执行成功' : '❌ 执行失败';
    html += '</div>';

    if (result.error) {
        html += '<div style="margin-bottom:12px;color:#be4334;">' + result.error + '</div>';
    }

    html += '<div style="font-size:12px;color:#697063;margin-bottom:12px;">';
    html += '共读取 ' + result.lines_read + ' 行，执行 ' + result.steps_run + ' 步';
    html += '</div>';

    result.steps.forEach(step => {
        const stepClass = step.ok ? 'script-step' : 'script-step error';
        html += '<div class="' + stepClass + '">';
        html += '<div class="script-step-header">';
        html += (step.ok ? '✓' : '✗') + ' Line ' + step.line + ': ' + (step.type || 'unknown');
        if (step.tool) html += ' → ' + step.tool;
        if (step.text) html += ' → "' + escapeHtml(step.text) + '"';
        html += ' (' + step.duration_ms + 'ms)';
        html += '</div>';
        if (step.output) {
            let outputText = step.output;
            if (outputText.length > 200) {
                outputText = outputText.substring(0, 200) + '...';
            }
            html += '<div class="script-step-output">输出: ' + escapeHtml(outputText) + '</div>';
        }
        if (step.error) {
            html += '<div class="script-step-output" style="color:#be4334;">错误: ' + escapeHtml(step.error) + '</div>';
        }
        html += '</div>';
    });

    scriptResult.innerHTML = html;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

console.log('🎯 归一化坐标调试工具已加载');
</script>
</body>
</html>
`
