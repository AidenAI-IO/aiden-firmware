import {byId, runtimeFunction} from './state.js';
import {request, setBanner, setDetails} from './api.js';

const t = runtimeFunction('t');

let editorSnapshot = '';
let loaded = false;

function setManualConfigLocked(locked) {
  const editor = byId('manualConfigContent');
  const save = byId('save-manual-config');
  if (editor) editor.disabled = locked;
  if (save) save.disabled = locked;
}

async function loadManualConfig(force = false) {
  const editor = byId('manualConfigContent');
  if (!editor || (loaded && !force)) return true;
  try {
    const response = await fetch('/api/config/backup', {method: 'GET'});
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || ('HTTP ' + response.status));
    }
    editor.value = await response.text();
    loaded = true;
    return true;
  } catch (err) {
    setBanner(t('manual_config.load_failed'), true);
    setDetails(err.message);
    return false;
  }
}

async function enterManualConfigEdit() {
  if (!await loadManualConfig(true)) return;
  const editor = byId('manualConfigContent');
  editorSnapshot = editor ? editor.value : '';
  setManualConfigLocked(false);
  byId('section-manual-config')?.classList.add('editing');
  setBanner(t('manual_config.editing'), false);
  setDetails('');
}

function cancelManualConfigEdit() {
  const editor = byId('manualConfigContent');
  if (editor) editor.value = editorSnapshot;
  setManualConfigLocked(true);
  byId('section-manual-config')?.classList.remove('editing');
  setBanner(t('manual_config.cancelled'), false);
  setDetails('');
}

async function saveManualConfig() {
  const editor = byId('manualConfigContent');
  if (!editor) return false;
  setManualConfigLocked(true);
  try {
    const payload = await request('/api/config/backup', {
      method: 'PUT',
      headers: {'Content-Type': 'application/toml; charset=utf-8'},
      body: editor.value
    });
    editorSnapshot = editor.value;
    loaded = true;
    byId('section-manual-config')?.classList.remove('editing');
    setBanner(t(payload.pending ? 'manual_config.saved_pending' : 'manual_config.saved'), false);
    setDetails('');
    return true;
  } catch (err) {
    if (err && err.persisted === true) {
      editorSnapshot = editor.value;
      loaded = true;
      byId('section-manual-config')?.classList.remove('editing');
      setBanner(t('manual_config.saved_not_applied'), true);
      setDetails(err.message);
      return true;
    }
    setManualConfigLocked(false);
    setBanner(t('manual_config.save_failed'), true);
    setDetails(err.message);
    return false;
  }
}

export {
  cancelManualConfigEdit, enterManualConfigEdit, loadManualConfig,
  saveManualConfig, setManualConfigLocked
};
