#pragma once

static const char* HID_SERVER_HTML = R"HTML(
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Aiden HID Test</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #1a1a1a; color: #e0e0e0; padding: 20px; }
h1 { text-align: center; margin-bottom: 30px; color: #4a9eff; }
h2 { margin: 20px 0 15px; color: #6ab7ff; font-size: 1.3em; }
.container { max-width: 900px; margin: 0 auto; }
.section { background: #252525; padding: 20px; border-radius: 8px; margin-bottom: 20px; }
.input-group { display: flex; gap: 10px; margin-bottom: 15px; align-items: center; }
input[type="text"], input[type="number"] { flex: 1; padding: 10px; background: #333; border: 1px solid #444; border-radius: 4px; color: #e0e0e0; font-size: 14px; }
input[type="text"]:focus, input[type="number"]:focus { outline: none; border-color: #4a9eff; }
button { padding: 10px 16px; background: #4a9eff; border: none; border-radius: 4px; color: white; cursor: pointer; font-size: 14px; font-weight: 500; transition: all 0.2s; }
button:hover { background: #3a8eef; transform: translateY(-1px); }
button:active { transform: translateY(0); background: #2a7edf; }
.btn-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(80px, 1fr)); gap: 8px; margin-bottom: 15px; }
.btn-small { padding: 8px 12px; font-size: 13px; }
.btn-compact { padding: 6px 10px; font-size: 12px; }
.touchpad { width: 100%; max-width: 500px; height: 300px; background: #1a1a1a; border: 2px solid #4a9eff; border-radius: 8px; margin: 15px auto; cursor: crosshair; position: relative; user-select: none; touch-action: none; }
.touchpad:active { border-color: #6ab7ff; }
.coords { text-align: center; margin: 10px 0; color: #888; font-family: monospace; }
#log { background: #1a1a1a; padding: 15px; border-radius: 4px; max-height: 200px; overflow-y: auto; font-family: monospace; font-size: 12px; }
.log-entry { padding: 4px 0; border-bottom: 1px solid #333; }
.log-entry:last-child { border-bottom: none; }
</style>
</head>
<body>
<div class="container">
<h1>Aiden HID Test</h1>

<div class="section">
<h2>⌨️ Keyboard</h2>
<div class="input-group">
<input type="text" id="textInput" placeholder="Type text to send...">
<button onclick="sendText()">Type</button>
</div>
<div class="btn-grid">
<button class="btn-small" onclick="tap('ENTER')">Enter</button>
<button class="btn-small" onclick="tap('TAB')">Tab</button>
<button class="btn-small" onclick="tap('BACKSPACE')">Backspace</button>
<button class="btn-small" onclick="tap('ESCAPE')">Escape</button>
<button class="btn-small" onclick="tap('SPACE')">Space</button>
<button class="btn-small" onclick="tap('DELETE')">Delete</button>
</div>
<div class="btn-grid">
<button class="btn-small" onclick="tap('UP')">↑ Up</button>
<button class="btn-small" onclick="tap('DOWN')">↓ Down</button>
<button class="btn-small" onclick="tap('LEFT')">← Left</button>
<button class="btn-small" onclick="tap('RIGHT')">→ Right</button>
</div>
<div class="btn-grid">
<button class="btn-compact" onclick="tap('CTRL','C')">Ctrl+C</button>
<button class="btn-compact" onclick="tap('CTRL','V')">Ctrl+V</button>
<button class="btn-compact" onclick="tap('CTRL','Z')">Ctrl+Z</button>
<button class="btn-compact" onclick="tap('CTRL','A')">Ctrl+A</button>
<button class="btn-compact" onclick="tap('CTRL','S')">Ctrl+S</button>
<button class="btn-compact" onclick="tap('ALT','TAB')">Alt+Tab</button>
<button class="btn-compact" onclick="tap('ALT','F4')">Alt+F4</button>
</div>
<div class="btn-grid">
<button class="btn-compact" onclick="tap('F1')">F1</button>
<button class="btn-compact" onclick="tap('F2')">F2</button>
<button class="btn-compact" onclick="tap('F3')">F3</button>
<button class="btn-compact" onclick="tap('F4')">F4</button>
<button class="btn-compact" onclick="tap('F5')">F5</button>
<button class="btn-compact" onclick="tap('F6')">F6</button>
<button class="btn-compact" onclick="tap('F7')">F7</button>
<button class="btn-compact" onclick="tap('F8')">F8</button>
<button class="btn-compact" onclick="tap('F9')">F9</button>
<button class="btn-compact" onclick="tap('F10')">F10</button>
<button class="btn-compact" onclick="tap('F11')">F11</button>
<button class="btn-compact" onclick="tap('F12')">F12</button>
</div>
<div class="input-group">
<input type="text" id="customKey" placeholder="Key name (e.g. HOME, PAGEUP)">
<button onclick="tapCustom()">Tap</button>
</div>
</div>

<div class="section">
<h2>🖱 Mouse</h2>
<div class="touchpad" id="touchpad"></div>
<div class="coords" id="coordsDisplay">Drag to move cursor, click to left-click</div>
<div class="btn-grid">
<button class="btn-small" onclick="mouseClick('left')">Left Click</button>
<button class="btn-small" onclick="mouseClick('right')">Right Click</button>
<button class="btn-small" onclick="mouseClick('middle')">Middle Click</button>
<button class="btn-small" onclick="mouseScroll(-3)">Scroll Up</button>
<button class="btn-small" onclick="mouseScroll(3)">Scroll Down</button>
</div>
<div class="input-group">
<input type="number" id="moveDx" placeholder="dX (-127~127)" min="-127" max="127">
<input type="number" id="moveDy" placeholder="dY (-127~127)" min="-127" max="127">
<button onclick="manualMove()">Move</button>
</div>
</div>

<div class="section">
<h2>Log</h2>
<div id="log"></div>
</div>
</div>

<script>
function log(msg) {
    const el = document.getElementById('log');
    const t = new Date().toLocaleTimeString();
    el.innerHTML = '<div class="log-entry">[' + t + '] ' + msg + '</div>' + el.innerHTML;
    if (el.children.length > 50) el.lastChild.remove();
}

async function api(endpoint, body) {
    try {
        const res = await fetch(endpoint, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
        });
        const text = await res.text();
        log(endpoint + ' → ' + text);
    } catch (e) {
        log(endpoint + ' ERROR: ' + e.message);
    }
}

function tap(...keys) { api('/api/keyboard/tap', {keys: keys}); }

function sendText() {
    const v = document.getElementById('textInput').value;
    if (v) api('/api/keyboard/text', {text: v});
}

function tapCustom() {
    const v = document.getElementById('customKey').value.trim().toUpperCase();
    if (v) api('/api/keyboard/tap', {keys: v.split('+').map(s => s.trim())});
}

function mouseClick(btn) { api('/api/touch/click', {button: btn}); }
function mouseScroll(amt) { api('/api/touch/scroll', {amount: amt}); }

function manualMove() {
    const dx = parseInt(document.getElementById('moveDx').value);
    const dy = parseInt(document.getElementById('moveDy').value);
    if (!isNaN(dx) && !isNaN(dy)) api('/api/touch/move', {dx, dy});
}

(function() {
    const pad = document.getElementById('touchpad');
    const display = document.getElementById('coordsDisplay');
    let dragging = false, lastX = 0, lastY = 0;

    pad.addEventListener('pointerdown', function(e) {
        dragging = true;
        lastX = e.clientX;
        lastY = e.clientY;
        pad.setPointerCapture(e.pointerId);
        e.preventDefault();
    });

    pad.addEventListener('pointermove', function(e) {
        if (!dragging) return;
        const dx = Math.round(e.clientX - lastX);
        const dy = Math.round(e.clientY - lastY);
        lastX = e.clientX;
        lastY = e.clientY;
        if (dx !== 0 || dy !== 0) {
            const clamp = v => Math.max(-127, Math.min(127, v));
            display.textContent = 'Move: (' + dx + ', ' + dy + ')';
            api('/api/touch/move', {dx: clamp(dx), dy: clamp(dy)});
        }
        e.preventDefault();
    });

    pad.addEventListener('pointerup', function(e) {
        if (dragging && Math.abs(e.clientX - lastX) < 3 && Math.abs(e.clientY - lastY) < 3) {
            api('/api/touch/click', {button: 'left'});
            display.textContent = 'Click';
        }
        dragging = false;
        e.preventDefault();
    });
})();
</script>
</body>
</html>
)HTML";
