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
<div class="info-item"><span class="info-label">截图来源:</span><span class="info-value" id="captureBackend">-</span></div>
<div class="info-item"><span class="info-label">ADB 设备:</span><span class="info-value" id="adbDeviceInfo">-</span></div>
<div class="info-item"><span class="info-label">点击位置 (像素):</span><span class="info-value" id="clickPixel">-</span></div>
<div class="info-item"><span class="info-label">归一化坐标:</span><span class="info-value" id="normalizedCoord">-</span></div>
<div class="info-item"><span class="info-label">标记位置 (像素):</span><span class="info-value" id="markerPixel">-</span></div>
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
        original_screen_height_pixels: null,
        capture_backend: null,
        adb_device: null
    };
}

function formatOriginalScreenSize(meta) {
    if (!meta) return '-';
    const width = meta.original_screen_width_pixels;
    const height = meta.original_screen_height_pixels;
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) return '-';
    return width + ' × ' + height + ' px';
}

function formatCaptureBackend(meta) {
    if (!meta || !meta.capture_backend) return '-';
    return String(meta.capture_backend);
}

function formatADBDevice(meta) {
    if (!meta || !meta.adb_device) return '-';
    const device = meta.adb_device;
    const name = device.name || device.serial || '-';
    const label = device.serial && device.serial !== name ? (name + ' (' + device.serial + ')') : name;
    return device.state ? (label + ' / ' + device.state) : label;
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
            capture_backend: res.headers.get('X-Capture-Backend') || null,
            adb_device: res.headers.get('X-Adb-Device-Valid') === 'true' ? {
                serial: res.headers.get('X-Adb-Device-Serial') || '',
                name: res.headers.get('X-Adb-Device-Name') || '',
                state: res.headers.get('X-Adb-Device-State') || ''
            } : null,
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
            document.getElementById('captureBackend').textContent = formatCaptureBackend(currentScreenshotMeta);
            document.getElementById('adbDeviceInfo').textContent = formatADBDevice(currentScreenshotMeta);
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

console.log('🎯 归一化坐标调试工具已加载');
</script>
</body>
</html>
`
