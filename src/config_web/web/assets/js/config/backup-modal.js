// Wizard DOM for the Backup & Restore card.  backup.js owns the protocol and
// state; this module only renders steps, component lists and progress.
import {byId, runtimeFunction} from './state.js';

const t = runtimeFunction('t');
const STEPS = ['backup-intro', 'restore-intro', 'progress', 'done'];

const modal = () => byId('dataBackupModal');

export function openBackupModal(kind) {
  const element = modal();
  if (!element) return;
  element.dataset.kind = kind;
  element.classList.add('show');
  element.setAttribute('aria-hidden', 'false');
  const title = byId('dataBackupModalTitle');
  if (title) title.textContent = t(kind === 'restore' ? 'backup.restore_title' : 'backup.create_title');
  setBackupStatus('');
}

export function closeBackupModal() {
  const element = modal();
  if (!element) return;
  element.classList.remove('show');
  element.setAttribute('aria-hidden', 'true');
}

export function isBackupModalOpen() {
  const element = modal();
  return !!(element && element.classList.contains('show'));
}

export function showStep(step) {
  STEPS.forEach(name => {
    const element = byId(`dataBackupStep-${name}`);
    if (element) element.hidden = name !== step;
  });
}

export function setBackupStatus(message, error = false) {
  const element = byId('dataBackupStatus');
  if (!element) return;
  element.textContent = message || '';
  element.className = `fw-health${error ? ' error' : ''}`;
  element.style.display = message ? 'block' : 'none';
}

// setCardStatus mirrors the last operation onto the Storage Settings card so
// it survives closing the wizard.
export function setCardStatus(message, error = false) {
  const element = byId('dataBackupCardStatus');
  if (!element) return;
  element.textContent = message || '';
  element.className = `fw-health${error ? ' error' : ''}`;
  element.style.display = message ? 'block' : 'none';
}

// Progress is reported as text only (phase and byte counts); there is no
// progress bar in the wizard or on the card.
export function setBackupProgress(current, total, message, indeterminate = false) {
  const percent = total > 0 ? Math.min(100, Math.round(current * 100 / total)) : 0;
  [byId('dataBackupProgressText'), byId('dataBackupCardProgressText')].forEach(text => {
    if (text) text.textContent = message || (indeterminate ? '' : `${percent}%`);
  });
}

export function hideCardProgress() {
  const text = byId('dataBackupCardProgressText');
  if (text) text.textContent = '';
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let index = 0;
  let amount = bytes;
  while (amount >= 1024 && index < units.length - 1) { amount /= 1024; index += 1; }
  return `${amount.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

export {formatBytes};

function componentLabel(id) {
  return t(`backup.component.${id}`, {defaultValue: id});
}

// renderComponents draws checkboxes for the regular components and folds the
// advanced ones (Python environment, diagnostics, whole-card files) into a
// collapsed details block.
export function renderComponents(containerId, components, selected = [], options = {}) {
  const container = byId(containerId);
  if (!container) return;
  const selectedSet = new Set(selected);
  container.textContent = '';
  const regular = document.createElement('div');
  regular.className = 'backup-components';
  const advanced = document.createElement('div');
  advanced.className = 'backup-components';
  let advancedCount = 0;
  (components || []).forEach(component => {
    const label = document.createElement('label');
    label.className = 'backup-component';
    const input = document.createElement('input');
    input.type = 'checkbox';
    input.value = component.id;
    input.dataset.component = component.id;
    const available = component.available !== false && !(options.sameDeviceOnlyDisabled && component.same_device_only);
    input.checked = available && selectedSet.has(component.id);
    input.disabled = !available;
    const text = document.createElement('span');
    text.textContent = componentLabel(component.id);
    const detail = document.createElement('small');
    const notes = [];
    if (component.estimated_size != null) notes.push(formatBytes(component.estimated_size));
    if (component.expanded_size != null) notes.push(formatBytes(component.expanded_size));
    if (component.file_count != null) notes.push(t('backup.files_count', {count: component.file_count}));
    if (component.sensitive) notes.push(t('backup.sensitive'));
    if (component.same_device_only) notes.push(t('backup.same_device_only'));
    if (!available) notes.push(t(component.same_device_only && options.sameDeviceOnlyDisabled ? 'backup.portable_excluded' : 'backup.unavailable'));
    detail.textContent = notes.join(' · ');
    text.appendChild(detail);
    label.append(input, text);
    if (component.advanced) { advanced.appendChild(label); advancedCount += 1; } else regular.appendChild(label);
  });
  container.appendChild(regular);
  if (advancedCount) {
    const details = document.createElement('details');
    details.className = 'backup-advanced';
    const summary = document.createElement('summary');
    summary.textContent = t('backup.advanced_components');
    details.append(summary, advanced);
    container.appendChild(details);
  }
}

export function selectedComponents(containerId) {
  return Array.from(document.querySelectorAll(`#${containerId} input[data-component]:checked`)).map(input => input.value);
}

export function renderManifestSummary(manifest, file) {
  const element = byId('dataRestoreManifestSummary');
  if (!element || !manifest) return;
  const source = manifest.source || {};
  const created = manifest.created_at ? new Date(manifest.created_at).toLocaleString() : t('backup.unknown_date');
  const lines = [
    t('backup.manifest_summary', {name: file?.name || '', size: formatBytes(file?.size), created, mode: t(`backup.mode.${manifest.mode}`, {defaultValue: manifest.mode || ''})}),
  ];
  if (source.firmware_version) lines.push(t('backup.manifest_firmware', {version: source.firmware_version}));
  const storage = manifest.storage || {};
  if (storage.sd_present) lines.push(t('backup.manifest_sd', {uuid: storage.sd_uuid || '?'}));
  if (manifest.conflicts) lines.push(t('backup.manifest_conflicts', {count: manifest.conflicts}));
  element.textContent = lines.join('\n');
}

export function setRestoreFileInfo(file) {
  const element = byId('dataRestoreFileInfo');
  if (element) element.textContent = file ? t('backup.file_info', {name: file.name, size: formatBytes(file.size)}) : '';
}

export function setDoneMessage(message, error = false) {
  const element = byId('dataBackupDoneText');
  if (element) {
    element.textContent = message || '';
    element.className = `field-hint${error ? ' error' : ''}`;
  }
}
