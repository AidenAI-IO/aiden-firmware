// Full data backup and restore (the /api/backup and /api/restore job APIs).
// The Agent-config-only TOML export/import stays in storage.js and keeps
// using /api/config/backup.
import {request, setDetails} from './api.js';
import {byId, appState, runtimeFunction} from './state.js';
import {
  openBackupModal, closeBackupModal, isBackupModalOpen, showStep, setBackupStatus, setCardStatus,
  setBackupProgress, hideCardProgress, renderComponents, selectedComponents, renderManifestSummary,
  renderRestoreConfirm, setRestoreFileInfo, setDoneMessage, formatBytes,
} from './backup-modal.js';
import {
  hasNativeTransfer, requestNativeDownload, requestNativeFile, requestNativeUpload,
  cancelNativeTransfer, readArchiveHeader, triggerBrowserDownload, uploadBrowserChunks,
} from './host-transfer.js';

const t = runtimeFunction('t');
const TERMINAL = new Set(['completed', 'failed', 'cancelled', 'reboot_required', 'rollback_failed']);
const MAINTENANCE_LOCKED_ACTIONS = [
  'format-storage', 'eject-storage', 'choose-config-backup', 'export-config-backup', 'ota-update',
  'reboot-device', 'reset-conversation-memory', 'apply-system-env', 'save-system-env',
];
const MAINTENANCE_LOCKED_IDS = ['dataBackupExportBtn', 'dataRestoreImportBtn'];

let maintenanceSession = null;
let activeJob = null;      // {kind: 'backup'|'restore', job_id, transfer_token, state, ...}
let activeFile = null;
let planGate = null;       // deferred resolved by continueDataRestore
let confirmGate = null;    // deferred resolved by confirmDataRestore
let pollTimer = null;
let usbBlocked = false;

function requestID() {
  return crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail; });
  return {promise, resolve, reject};
}

function delay(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }

function errorText(error) {
  if (!error) return '';
  const code = error.error || error.code || error.message;
  const known = t(`backup.error.${code}`, {defaultValue: ''});
  if (known) return known;
  return error.message || String(code || 'error');
}

// --- maintenance session -------------------------------------------------

async function openMaintenanceSession() {
  if (maintenanceSession && new Date(maintenanceSession.expires_at).getTime() > Date.now() + 30000) return maintenanceSession;
  try {
    maintenanceSession = await request('/api/maintenance/sessions', {
      method: 'POST', credentials: 'same-origin', headers: {'X-Aiden-Client': 'config-web/1.0'},
    });
  } catch (error) {
    if (error.error === 'usb_required') {
      usbBlocked = true;
      applyMaintenanceLock();
      setCardStatus(t('backup.usb_required'), true);
    }
    throw error;
  }
  usbBlocked = false;
  return maintenanceSession;
}

async function maintenanceRequest(url, options = {}) {
  const session = await openMaintenanceSession();
  const headers = new Headers(options.headers || {});
  headers.set('X-Aiden-Request-ID', requestID());
  headers.set('X-Aiden-CSRF-Token', session.csrf_token);
  try {
    return await request(url, {...options, headers, credentials: 'same-origin'});
  } catch (error) {
    if (error.status === 423) await attachMaintenance(error);
    throw error;
  }
}

async function jobRequest(url, options = {}) {
  const headers = new Headers(options.headers || {});
  if (activeJob?.transfer_token) headers.set('Authorization', `Bearer ${activeJob.transfer_token}`);
  return request(url, {...options, headers, credentials: 'same-origin'});
}

function jobPath(kind, id, suffix = '') {
  return `/api/${kind}/jobs/${encodeURIComponent(id)}${suffix}`;
}

// --- page state --------------------------------------------------------

function applyMaintenanceLock() {
  const locked = !!(activeJob && !TERMINAL.has(activeJob.state));
  document.body.classList.toggle('maintenance-active', locked);
  MAINTENANCE_LOCKED_ACTIONS.forEach(action => {
    document.querySelectorAll(`[data-action="${action}"]`).forEach(button => {
      if (locked) { button.dataset.maintenanceLocked = '1'; button.disabled = true; }
      else if (button.dataset.maintenanceLocked) { delete button.dataset.maintenanceLocked; button.disabled = false; }
    });
  });
  MAINTENANCE_LOCKED_IDS.forEach(id => {
    const button = byId(id);
    if (button) button.disabled = locked || usbBlocked;
  });
  const banner = byId('dataBackupMaintenanceBanner');
  if (banner) {
    banner.hidden = !locked;
    if (locked) banner.textContent = t(activeJob.kind === 'restore' ? 'backup.maintenance_restore' : 'backup.maintenance_backup');
  }
}

function setActiveJob(job) {
  activeJob = job;
  appState.backupJob = job;
  applyMaintenanceLock();
}

function updateJob(payload) {
  if (!payload || !activeJob) return;
  setActiveJob({...activeJob, ...payload});
}

function phaseText(payload) {
  const phase = payload.phase || payload.state || '';
  const known = t(`backup.phase.${phase}`, {defaultValue: ''});
  if (known) return known;
  const component = t(`backup.component.${phase}`, {defaultValue: ''});
  return component || phase;
}

function renderJobProgress(payload) {
  const state = payload.state || '';
  if (activeJob?.kind === 'backup') {
    const total = Number(payload.estimated_bytes || 0);
    const done = Number(payload.bytes_read || 0);
    if (state === 'streaming' && total > 0) setBackupProgress(done, total, t('backup.progress_streaming', {done: formatBytes(done), total: formatBytes(total), phase: phaseText(payload)}));
    else setBackupProgress(0, 0, phaseText(payload), !TERMINAL.has(state));
  } else {
    const total = Number(payload.archive_size || activeFile?.size || 0);
    const done = Number(payload.received_bytes || 0);
    if (['restore_ingest', 'reading_manifest', 'uploading_and_staging'].includes(state) && total > 0) {
      setBackupProgress(done, total, t('backup.progress_uploading', {done: formatBytes(done), total: formatBytes(total), phase: phaseText(payload)}));
    } else setBackupProgress(0, 0, phaseText(payload), !TERMINAL.has(state));
  }
}

function finishJob(payload) {
  updateJob(payload);
  const state = payload.state;
  const error = payload.error ? errorText(payload.error) : '';
  let message;
  let isError = false;
  switch (state) {
    case 'completed':
      message = t(activeJob.kind === 'restore' ? 'backup.restore_completed' : 'backup.create_completed');
      break;
    case 'reboot_required':
      message = t('backup.restore_reboot_required');
      break;
    case 'cancelled':
      message = t('backup.cancelled');
      break;
    case 'rollback_failed':
      message = t('backup.rollback_failed', {error});
      isError = true;
      break;
    default:
      message = t(activeJob.kind === 'restore' ? 'backup.restore_failed' : 'backup.create_failed', {error});
      isError = true;
  }
  const warnings = (payload.warnings || []).join('\n');
  setCardStatus(warnings ? `${message}\n${warnings}` : message, isError);
  hideCardProgress();
  if (isBackupModalOpen()) {
    setDoneMessage(warnings ? `${message}\n${warnings}` : message, isError);
    showStep('done');
  }
  if (state === 'reboot_required') scheduleReconnect();
  applyMaintenanceLock();
}

async function pollJob(kind, jobId) {
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
  for (let attempt = 0; attempt < 4 * 60 * 60; attempt += 1) {
    if (!activeJob || activeJob.job_id !== jobId) return null;
    let payload;
    try {
      payload = await jobRequest(jobPath(kind, jobId));
    } catch (error) {
      if (error.status === 404) throw error;
      await delay(2000);
      continue;
    }
    updateJob(payload);
    renderJobProgress(payload);
    if (TERMINAL.has(payload.state)) { finishJob(payload); return payload; }
    await delay(1000);
  }
  throw new Error(t('backup.error.timeout'));
}

function scheduleReconnect() {
  const notice = byId('dataBackupReconnectNotice');
  if (notice) notice.hidden = false;
  let attempts = 0;
  const check = async () => {
    attempts += 1;
    try {
      await fetch('/api/device/snapshot', {cache: 'no-store'});
      if (attempts > 3) { if (notice) notice.textContent = t('backup.reconnected'); return; }
    } catch (_error) { /* device still rebooting */ }
    if (attempts < 300) setTimeout(check, 3000);
  };
  setTimeout(check, 15000);
}

// --- create backup ----------------------------------------------------

export async function createDataBackup() {
  if (activeJob && !TERMINAL.has(activeJob.state)) { openBackupModal(activeJob.kind); showStep('progress'); return; }
  openBackupModal('backup');
  showStep('backup-intro');
  setBackupStatus(t('backup.loading_capabilities'));
  const start = byId('dataBackupStartBtn');
  if (start) start.disabled = true;
  try {
    await openMaintenanceSession();
    const capabilities = await maintenanceRequest('/api/backup/capabilities');
    appState.backupCapabilities = capabilities;
    const mode = byId('dataBackupMode');
    if (mode) mode.value = capabilities.device?.identity_available === false ? 'portable' : 'same_device';
    renderBackupComponents();
    const sd = capabilities.sd || {};
    const hint = byId('dataBackupSdHint');
    if (hint) hint.textContent = sd.mounted ? t('backup.sd_included', {device: sd.device || ''}) : t('backup.sd_absent');
    setBackupStatus('');
    if (start) start.disabled = false;
  } catch (error) {
    setBackupStatus(errorText(error), true);
  }
}

function renderBackupComponents() {
  const capabilities = appState.backupCapabilities || {};
  const mode = byId('dataBackupMode')?.value || 'same_device';
  const components = capabilities.components || [];
  const defaults = components.filter(item => item.default_selected && item.available !== false && (mode === 'same_device' || !item.same_device_only)).map(item => item.id);
  renderComponents('dataBackupComponents', components, defaults, {sameDeviceOnlyDisabled: mode !== 'same_device'});
}

export async function startDataBackup() {
  const passphrase = byId('dataBackupPassphrase')?.value || '';
  const confirmation = byId('dataBackupPassphraseConfirm')?.value || '';
  if (passphrase.length < 8) { setBackupStatus(t('backup.passphrase_too_short'), true); return; }
  if (passphrase !== confirmation) { setBackupStatus(t('backup.passphrase_mismatch'), true); return; }
  const components = selectedComponents('dataBackupComponents');
  if (!components.length) { setBackupStatus(t('backup.no_components'), true); return; }
  const mode = byId('dataBackupMode')?.value || 'same_device';
  const button = byId('dataBackupStartBtn');
  if (button) button.disabled = true;
  try {
    const response = await maintenanceRequest('/api/backup/jobs', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({format_version: 1, mode, components, protection: {mode: 'passphrase', passphrase}}),
    });
    byId('dataBackupPassphrase').value = '';
    byId('dataBackupPassphraseConfirm').value = '';
    setActiveJob({kind: 'backup', ...response});
    showStep('progress');
    setBackupStatus('');
    setBackupProgress(0, 0, t('backup.phase.ready'), true);
    setCardStatus(t('backup.create_started'));
    const url = response.archive_url;
    if (!requestNativeDownload({job_id: response.job_id, url, filename: response.suggested_filename, transfer_token: response.transfer_token})) {
      triggerBrowserDownload(url, response.suggested_filename);
    }
    await pollJob('backup', response.job_id);
  } catch (error) {
    setBackupStatus(errorText(error), true);
    setDetails(error.message);
    if (activeJob && !TERMINAL.has(activeJob.state)) setActiveJob({...activeJob, state: 'failed'});
    applyMaintenanceLock();
  } finally {
    if (button) button.disabled = false;
  }
}

// --- restore -------------------------------------------------------

export function chooseDataRestore() {
  if (activeJob && !TERMINAL.has(activeJob.state)) { openBackupModal(activeJob.kind); showStep('progress'); return; }
  if (requestNativeFile()) return;
  const input = byId('dataRestoreInput');
  if (input) input.click();
}

export async function restoreFileSelected(file) {
  if (!file) return;
  activeFile = file;
  openBackupModal('restore');
  showStep('restore-intro');
  setRestoreFileInfo(file);
  const start = byId('dataRestoreStartBtn');
  if (start) start.disabled = true;
  try {
    await openMaintenanceSession();
    activeFile.header = file.header || await readArchiveHeader(file);
    const created = activeFile.header.created_at ? new Date(activeFile.header.created_at).toLocaleString() : '';
    setBackupStatus(created ? t('backup.archive_created', {created}) : '');
    if (start) start.disabled = false;
  } catch (error) {
    setBackupStatus(errorText(error), true);
  }
}

export async function startDataRestore() {
  const file = activeFile;
  if (!file || !file.header) return;
  const passphrase = byId('dataRestorePassphrase')?.value || '';
  if (passphrase.length < 8) { setBackupStatus(t('backup.passphrase_too_short'), true); return; }
  const button = byId('dataRestoreStartBtn');
  if (button) button.disabled = true;
  try {
    const created = await maintenanceRequest('/api/restore/jobs', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({format_version: 1, archive_size: file.size, public_header: file.header, protection: {mode: 'passphrase', passphrase}}),
    });
    byId('dataRestorePassphrase').value = '';
    setActiveJob({kind: 'restore', ...created, archive_size: file.size});
    showStep('progress');
    setBackupStatus('');
    setCardStatus(t('backup.restore_started'));
    setBackupProgress(0, file.size, t('backup.phase.restore_ingest'));
    if (file.native && hasNativeTransfer()) {
      // Bridge App: the native module streams the file and reports progress
      // through postMessage; the page only drives plan/validate/apply.
      requestNativeUpload({job_id: created.job_id, chunk_size: created.chunk_size, transfer_token: created.transfer_token});
      await waitForNativeUpload(created.job_id);
    } else {
      await uploadBrowserChunks(file, created.chunk_size, (index, chunk, hash) => jobRequest(jobPath('restore', created.job_id, `/chunks/${index}`), {
        method: 'PUT', headers: {'Content-Type': 'application/octet-stream', 'X-Aiden-Chunk-SHA256': hash}, body: chunk,
      }), handleChunkResponse);
    }
    await validateAndApply(created.job_id);
  } catch (error) {
    if (activeJob?.job_id && !['committing', 'post_processing', 'resuming_services', 'rolling_back'].includes(activeJob.state) && !TERMINAL.has(activeJob.state)) {
      try { await maintenanceRequest(jobPath('restore', activeJob.job_id), {method: 'DELETE'}); } catch (_ignored) { /* already finished */ }
    }
    setBackupStatus(errorText(error), true);
    setCardStatus(t('backup.restore_failed', {error: errorText(error)}), true);
    hideCardProgress();
    setDetails(error.message);
    if (activeJob && !TERMINAL.has(activeJob.state)) setActiveJob({...activeJob, state: 'failed'});
    setDoneMessage(t('backup.restore_failed', {error: errorText(error)}), true);
    showStep('done');
    applyMaintenanceLock();
  } finally {
    if (button) button.disabled = false;
  }
}

async function handleChunkResponse(response, offset, total) {
  updateJob(response);
  if (response.error) throw Object.assign(new Error(response.error.message || response.error.code), {error: response.error.code});
  renderJobProgress({...response, received_bytes: offset, archive_size: total});
  if (response.state === 'awaiting_plan') await presentRestorePlan(response);
}

async function waitForNativeUpload(jobId) {
  // The native module uploads; poll the job so the page can pause it at
  // awaiting_plan and detect completion of staging.
  for (;;) {
    const payload = await jobRequest(jobPath('restore', jobId));
    updateJob(payload);
    renderJobProgress(payload);
    if (payload.error) throw Object.assign(new Error(payload.error.message || payload.error.code), {error: payload.error.code});
    if (payload.state === 'awaiting_plan' && !activeJob.plan_digest) await presentRestorePlan(payload);
    if (payload.state === 'validating' || payload.state === 'prepared') return;
    if (TERMINAL.has(payload.state)) throw new Error(t('backup.error.upload_interrupted'));
    await delay(1000);
  }
}

async function presentRestorePlan(payload) {
  const manifest = payload.manifest || (await jobRequest(jobPath('restore', activeJob.job_id))).manifest;
  const components = manifest?.components || [];
  renderManifestSummary({...manifest, conflicts: manifest?.conflicts}, activeFile);
  renderComponents('dataRestoreComponents', components, components.map(item => item.id));
  const conflictRow = byId('dataRestoreConflictRow');
  if (conflictRow) conflictRow.hidden = !manifest?.conflicts;
  const identityRow = byId('dataRestoreIdentityRow');
  if (identityRow) identityRow.hidden = !components.some(item => item.id === 'device_identity');
  const sdRow = byId('dataRestoreSDRow');
  if (sdRow) sdRow.hidden = !components.some(item => item.id === 'sd_managed_audio' || item.id === 'sd_user_files');
  showStep('restore-plan');
  planGate = deferred();
  await planGate.promise;
  showStep('progress');
}

export async function continueDataRestore() {
  if (!activeJob || !planGate) return;
  const button = byId('dataRestorePlanBtn');
  if (button) button.disabled = true;
  try {
    const response = await maintenanceRequest(jobPath('restore', activeJob.job_id, '/plan'), {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        components: selectedComponents('dataRestoreComponents'),
        sd_strategy: byId('dataRestoreSDStrategy')?.value || 'require_match',
        confirm_conflicts: !!byId('dataRestoreConflictConfirm')?.checked,
        confirm_identity: !!byId('dataRestoreIdentityConfirm')?.checked,
      }),
    });
    updateJob(response);
    setBackupStatus('');
    planGate.resolve(response);
  } catch (error) {
    setBackupStatus(errorText(error), true);
  } finally {
    if (button) button.disabled = false;
  }
}

async function validateAndApply(jobId) {
  setBackupProgress(0, 0, t('backup.phase.validating'), true);
  const validated = await maintenanceRequest(jobPath('restore', jobId, '/validate'), {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
  updateJob(validated);
  renderRestoreConfirm({...activeJob, ...validated, components: activeJob?.components || validated.components});
  showStep('restore-confirm');
  confirmGate = deferred();
  await confirmGate.promise;
  showStep('progress');
  setBackupProgress(0, 0, t('backup.phase.committing'), true);
  let applied;
  try {
    applied = await maintenanceRequest(jobPath('restore', jobId, '/apply'), {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({plan_digest: validated.plan_digest, confirm: 'RESTORE'}),
    });
  } catch (error) {
    // A dropped apply response does not stop the commit on the device; only
    // a definite rejection (bad digest, not prepared) is a real failure.
    if (error.status && error.status < 500 && error.error !== 'restore_committing') throw error;
    await pollJob('restore', jobId);
    return;
  }
  updateJob(applied);
  if (TERMINAL.has(applied.state)) finishJob(applied);
  else await pollJob('restore', jobId);
}

export function confirmDataRestore() {
  if (!confirmGate) return;
  const typed = (byId('dataRestoreConfirmInput')?.value || '').trim().toUpperCase();
  if (typed !== 'RESTORE') { setBackupStatus(t('backup.confirm_word_required'), true); return; }
  setBackupStatus('');
  confirmGate.resolve(true);
  confirmGate = null;
}

// --- cancel / close / attach ------------------------------------------

export async function cancelDataBackup() {
  if (!activeJob?.job_id || TERMINAL.has(activeJob.state)) { closeBackupModal(); return; }
  if (!window.confirm(t('backup.cancel_confirm'))) return;
  try {
    cancelNativeTransfer(activeJob.job_id);
    const payload = await maintenanceRequest(jobPath(activeJob.kind, activeJob.job_id), {method: 'DELETE'});
    if (planGate) planGate.reject(new Error(t('backup.cancelled')));
    if (confirmGate) confirmGate.reject(new Error(t('backup.cancelled')));
    updateJob(payload);
    if (TERMINAL.has(payload.state)) finishJob(payload);
  } catch (error) {
    setBackupStatus(errorText(error), true);
  }
}

export function closeDataBackup() {
  if (activeJob && !TERMINAL.has(activeJob.state) && !planGate && !confirmGate) {
    setBackupStatus(t('backup.still_running'), true);
    showStep('progress');
    return;
  }
  if (planGate || confirmGate) {
    // Leaving the wizard mid-plan abandons the upload: the device discards
    // staging when the job is deleted.
    cancelDataBackup();
    return;
  }
  closeBackupModal();
}

// attachMaintenance re-binds the page to a job that is already running on
// the device (page refresh, second tab, or a 423 from another endpoint).
export async function attachMaintenance(error) {
  // Status endpoints need no session; the session is only opened
  // opportunistically so cancel works when this page is the holder.
  try { await openMaintenanceSession(); } catch (_error) { /* another client may hold it */ }
  let current;
  try {
    current = await request('/api/maintenance/current', {credentials: 'same-origin'});
  } catch (_error) {
    return;
  }
  if (!current || !current.job_id) {
    if (activeJob && !TERMINAL.has(activeJob.state)) setActiveJob({...activeJob, state: 'failed'});
    return;
  }
  if (activeJob?.job_id === current.job_id) return;
  const kind = current.operation === 'restore' ? 'restore' : 'backup';
  setActiveJob({kind, job_id: current.job_id, ...(current.job || {})});
  setCardStatus(t(kind === 'restore' ? 'backup.restore_started' : 'backup.create_started'));
  if (error || isBackupModalOpen()) { openBackupModal(kind); showStep('progress'); }
  renderJobProgress(current.job || {state: current.phase});
  pollJob(kind, current.job_id).catch(err => setCardStatus(errorText(err), true));
}

export function handleHostTransferMessage(message) {
  if (!message || typeof message !== 'object') return;
  if (message.type === 'aiden_restore_file_selected') {
    // Native pickers hand over metadata only; bytes stay in the native module.
    restoreFileSelected({name: message.name, size: message.size, header: message.header, native: true});
    return;
  }
  if (!activeJob || message.job_id !== activeJob.job_id) return;
  if (message.type === 'aiden_transfer_progress') setBackupProgress(message.bytes, message.total, t('backup.progress_transfer', {done: formatBytes(message.bytes), total: formatBytes(message.total)}));
  if (message.type === 'aiden_transfer_failed') setBackupStatus(t('backup.error.host_transfer_unavailable', {error: message.error_code || ''}), true);
}

export function initBackup() {
  const input = byId('dataRestoreInput');
  if (input) input.addEventListener('change', () => { const file = input.files?.[0]; input.value = ''; restoreFileSelected(file); });
  const mode = byId('dataBackupMode');
  if (mode) mode.addEventListener('change', renderBackupComponents);
  window.addEventListener('message', event => {
    let message;
    try { message = typeof event.data === 'string' ? JSON.parse(event.data) : event.data; } catch (_error) { return; }
    handleHostTransferMessage(message);
  });
  window.addEventListener('beforeunload', event => {
    if (activeJob && !TERMINAL.has(activeJob.state)) { event.preventDefault(); event.returnValue = ''; }
  });
  appState.backup = {get activeJob() { return activeJob; }};
  // Re-attach to a running job after a refresh without creating a new one.
  attachMaintenance().catch(() => {});
}
