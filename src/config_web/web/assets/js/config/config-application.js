import {request} from './api.js';
import {t} from './i18n.js';
import {byId, registerRuntime} from './state.js';

let status = null;
let reading = false;
let saveGeneration = 0;
function render() {
  const panel = byId('configApplication');
  if (!panel) return;
  panel.style.display = status && status.state ? 'block' : 'none';
  if (!status) return;
  const failed = status.state === 'failed' || status.state === 'unavailable';
  const key = status.state === 'unavailable' ? 'config.apply_unavailable' : failed ? 'config.apply_failed' : status.pending ? 'config.apply_pending' : status.reboot_required ? 'config.apply_reboot' : 'config.apply_done';
  byId('configApplicationText').textContent = t(key) + (status.error ? ' ' + status.error : '');
  panel.className = 'banner' + (failed ? ' error' : '');
  byId('configApplyRetry').style.display = failed ? '' : 'none';
  byId('configReboot').style.display = status.reboot_required ? '' : 'none';
}
function configApplicationSaved(payload) {
  saveGeneration++;
  status = {state: payload.state || (payload.pending ? 'pending' : 'applied'), error:payload.error||'', pending: !!payload.pending, reboot_required: !!(payload.reboot_required || payload.restart_required)};
  render();
}
export async function refreshConfigApplication() {
  if (reading) return;
  reading = true;
  const generation = saveGeneration;
  try { const next = await request('/api/config/application', {method:'GET'}); if (generation === saveGeneration) status = next; }
  catch (err) { if (generation === saveGeneration) status = {...(status || {}), state:'unavailable', error:err.message}; }
  finally { reading = false; render(); }
}
export async function retryConfigApplication() {
  try {
    await request('/api/config', {method:'PATCH', headers:{'Content-Type':'application/json'}, body:JSON.stringify({config:{}})});
    await refreshConfigApplication();
  } catch (err) { status = {...status,state:'failed',error:err.message}; render(); }
}
registerRuntime({configApplicationSaved});
document.addEventListener('aiden:locale-changed', render);
