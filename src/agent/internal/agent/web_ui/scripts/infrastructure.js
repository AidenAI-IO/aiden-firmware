// Manual infrastructure test panel.
const infrastructureEndpointByTarget = {
    'hid-click': '/api/infrastructure-test/hid',
    'hid-output': '/api/infrastructure-test/hid',
    hdmi: '/api/infrastructure-test/hdmi',
    'audio-record': '/api/infrastructure-test/audio-record',
    'audio-playback': '/api/infrastructure-test/audio-playback',
    audio: '/api/infrastructure-test/audio'
};

async function runInfrastructureTest(target) {
    const endpoint = infrastructureEndpointByTarget[target];
    if (!endpoint) return;

    setInfrastructureButtonsDisabled(true);
    setInfrastructureStatus('Running ' + target + '...');
    clearInfrastructureResultPreview();
    infrastructureResultPanelEl.classList.add('hidden');

    try {
        const res = await fetch(endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(infrastructureRequestForTarget(target))
        });

        const data = await res.json().catch(function() {
            return null;
        });
        if (!res.ok) {
            const message = data && (data.error || data.message) ? (data.error || data.message) : ('HTTP ' + res.status);
            throw new Error(message);
        }
        renderInfrastructureResult(target, data || {});
    } catch (err) {
        console.error('Infrastructure test failed:', err);
        setInfrastructureStatus(infrastructureTargetLabel(target) + ' failed: ' + err.message, true);
        infrastructureResultPanelEl.classList.remove('hidden');
        infrastructureResultMetaEl.textContent = infrastructureTargetLabel(target) + ' · error';
        infrastructureResultOutputEl.textContent = err.message;
        clearInfrastructureResultPreview();
    } finally {
        setInfrastructureButtonsDisabled(false);
    }
}

function infrastructureRequestForTarget(target) {
    switch (target) {
    case 'hid-click':
        return { mode: 'click', x: 500, y: 500, button: 'left', hold_ms: 80 };
    case 'hid-output':
        return { mode: 'input', key: 'h' };
    case 'hdmi':
        return { timeout_ms: 3000, quality: 80 };
    case 'audio-record':
        return { duration_ms: 2000 };
    case 'audio-playback':
        return { duration_ms: 1000 };
    case 'audio':
        return { duration_ms: 1000, playback: true };
    default:
        return {};
    }
}

function renderInfrastructureResult(target, result) {
    infrastructureResultPanelEl.classList.remove('hidden');
    infrastructureResultMetaEl.textContent = [
        infrastructureTargetLabel(target),
        result.duration_ms + ' ms',
        result.ok ? 'ok' : 'error'
    ].join(' · ');
    infrastructureResultOutputEl.textContent = formatToolPayload(JSON.stringify(result || {}, null, 2));

    const screenshot = infrastructureScreenshotFromResult(result);
    if (screenshot) {
        infrastructurePreviewWrapEl.classList.remove('hidden');
        infrastructurePreviewEl.src = 'data:image/' + (screenshot.format || 'jpeg') + ';base64,' + screenshot.data;
    } else {
        clearInfrastructureResultPreview();
    }

    const message = result.message || (result.ok ? 'Done.' : 'Failed.');
    setInfrastructureStatus(infrastructureTargetLabel(target) + ': ' + message, !result.ok);
}

function infrastructureScreenshotFromResult(result) {
    if (!result || typeof result !== 'object' || !result.data) return null;
    if (typeof result.width !== 'number' && typeof result.height !== 'number' && typeof result.size !== 'number') return null;
    return result;
}

function clearInfrastructureResultPreview() {
    infrastructurePreviewWrapEl.classList.add('hidden');
    infrastructurePreviewEl.removeAttribute('src');
}

function resetInfrastructureResult() {
    infrastructureResultPanelEl.classList.add('hidden');
    infrastructureResultMetaEl.textContent = '';
    infrastructureResultOutputEl.textContent = '';
    clearInfrastructureResultPreview();
    setInfrastructureStatus('', false);
}

function setInfrastructureStatus(message, isError) {
    infrastructureStatusEl.textContent = message;
    infrastructureStatusEl.classList.toggle('error', !!isError);
}

function setInfrastructureButtonsDisabled(disabled) {
    const buttons = document.querySelectorAll('.infrastructure-btn');
    buttons.forEach(function(button) {
        button.disabled = disabled;
    });
}

function infrastructureTargetLabel(target) {
    switch (target) {
    case 'hid-click':
        return '点击';
    case 'hid-output':
        return '输出';
    case 'hdmi':
        return 'HDMI';
    case 'audio-record':
        return '录音';
    case 'audio-playback':
        return '播放';
    case 'audio':
        return '语音';
    default:
        return target;
    }
}
