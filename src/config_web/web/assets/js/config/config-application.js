import {request, updateActionCardVisibility} from './api.js';
import {t} from './i18n.js';
import {byId, registerRuntime} from './state.js';

let status = null;
let reading = false;
let saveGeneration = 0;
let visible = false;
let appliedTimer = null;

function clearAppliedTimer() {
  if (!appliedTimer) return;
  clearTimeout(appliedTimer);
  appliedTimer = null;
}

function isApplied(next) {
  return next && next.state === 'applied' && !next.pending && !next.reboot_required;
}

function applyStatus(next, showApplied) {
  const wasPending = !!(status && status.pending);
  const wasVisibleApplied = visible && isApplied(status);
  status = next;
  if (!status || !status.state) {
    clearAppliedTimer();
    visible = false;
  } else if (isApplied(status)) {
    if (showApplied || wasPending) {
      clearAppliedTimer();
      visible = true;
      appliedTimer = setTimeout(function() {
        visible = false;
        appliedTimer = null;
        render();
      }, 4000);
    } else if (!wasVisibleApplied) {
      visible = false;
    }
  } else {
    clearAppliedTimer();
    visible = true;
  }
  render();
}

function render() {
  const panel = byId('configApplication');
  if (!panel) return;
  panel.style.display = visible && status && status.state ? 'block' : 'none';
  if (!status) {
    updateActionCardVisibility();
    return;
  }
  const failed = status.state === 'failed' || status.state === 'unavailable';
  const key = status.state === 'unavailable'
    ? 'config.apply_unavailable'
    : failed
      ? 'config.apply_failed'
      : status.pending
        ? 'config.apply_pending'
        : status.reboot_required
          ? 'config.apply_reboot'
          : 'config.apply_done';
  byId('configApplicationText').textContent = t(key) + (status.error ? ' ' + status.error : '');
  panel.className = 'banner' + (failed ? ' error' : '');
  byId('configApplyRetry').style.display = failed ? '' : 'none';
  byId('configReboot').style.display = status.reboot_required ? '' : 'none';
  updateActionCardVisibility();
}
function configApplicationSaved(payload) {
  saveGeneration++;
  applyStatus({
    state: payload.state,
    error: payload.error || '',
    pending: !!payload.pending,
    reboot_required: !!payload.reboot_required
  }, true);
}
export async function refreshConfigApplication() {
  if (reading) return;
  reading = true;
  const generation = saveGeneration;
  try {
    const next = await request('/api/config/application', {method: 'GET'});
    if (generation === saveGeneration) applyStatus(next, false);
  } catch (err) {
    if (generation === saveGeneration) {
      applyStatus({...status, state: 'unavailable', error: err.message}, false);
    }
  } finally {
    reading = false;
  }
}
export async function retryConfigApplication() {
  try {
    await request('/api/config', {
      method: 'PATCH', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({config: {}})
    });
    await refreshConfigApplication();
  } catch (err) {
    applyStatus({...status, state: 'failed', error: err.message}, false);
  }
}
registerRuntime({configApplicationSaved});
document.addEventListener('aiden:locale-changed', render);
