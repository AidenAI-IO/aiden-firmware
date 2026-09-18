import {refreshConfigApplication, retryConfigApplication} from './config-application.js';
import {request, setBanner, setDetails} from './api.js';
import {bindFieldVisibility, hydrateSelectOptions} from './config-meta.js';
import {
  cancelEditSection, closeTestToast, disableAgentConfigEditing, enterEditSection,
  cancelEditSectionFields, enterEditSectionFields, initialReadyMessage, loadConfig, loadConfigMeta,
  lockAllSections, rebootDevice, saveFieldGroups, saveSection, saveSections, saveSectionFields, testSection
} from './config-form.js';
import {initI18n, saveLocale, t} from './i18n.js';
import {applyPendingAgentLogSnapshotIfIdle, exportLogs, refreshAgentLog, setAgentLogAutoScroll, syncAgentLogAutoScroll, toggleAgentLogAutoScroll} from './logs.js';
import {cancelManualConfigEdit, enterManualConfigEdit, loadManualConfig, saveManualConfig, setManualConfigLocked} from './manual-config.js';
import {refreshAgentStatus} from './agent-status.js';
import {refreshOtaLog, triggerOtaUpdate} from './ota.js';
import {
  deleteSelectedProvider, editSelectedProvider, handleProviderAction, initProviders,
  ModelProvidersManager, SttProvidersManager, TtsProvidersManager, VoiceModelProvidersManager
} from './providers.js';
import {appState, byId, configureTerminalLink} from './state.js';
import {
  chooseConfigBackup, ejectStorageCard, exportConfigBackup, refreshStorage,
  restoreConfigBackup, startStorageFormat
} from './storage.js';
import {toggleSTTTest} from './stt-test.js';
import {resetConversationMemory} from './memory.js';
import {applySystemEnv, refreshSystemEnvApplication, cancelSystemEnvEdit, enterSystemEnvEdit, handleSystemEnvEditorKeydown, saveSystemEnv, toggleSystemEnvComment} from './system-env.js';
import {closeWifiModal, connectSavedWifi, connectSelectedWifi, forgetWifi, openWifiModal, scanWifi, syncWifiProxyFields, toggleWifiListExpanded} from './wifi.js';
import {VISIBLE_FIELDS} from './field-visibility.js';
import {SECTION_TO_GROUP_MAP} from './config-groups.js';
import {cancelDataBackup, chooseDataRestore, closeDataBackup, createDataBackup, initBackup, startDataBackup, startDataRestore} from './backup.js';

const simpleActions = {
  'export-logs': exportLogs,
  'ota-update': triggerOtaUpdate,
  'refresh-ota-log': () => refreshOtaLog(true),
  'reload-all': reloadAll,
 'retry-config-apply': retryConfigApplication,
 'reboot-device': rebootDevice,
  'scan-wifi': () => scanWifi(false),
  'refresh-agent-status': () => refreshAgentStatus(true),
  'toggle-agent-log-auto-scroll': toggleAgentLogAutoScroll,
  'refresh-agent-log': () => refreshAgentLog(true),
  'toggle-stt-test': toggleSTTTest,
  'refresh-storage': () => refreshStorage(true),
  'format-storage': startStorageFormat,
  'eject-storage': ejectStorageCard,
  'export-config-backup': exportConfigBackup,
  'choose-config-backup': chooseConfigBackup,
  'create-data-backup': createDataBackup,
  'choose-data-restore': chooseDataRestore,
  'start-data-backup': startDataBackup,
  'start-data-restore': startDataRestore,
  'cancel-data-backup': cancelDataBackup,
  'close-data-backup': closeDataBackup,
  'edit-system-env': enterSystemEnvEdit,
  'toggle-system-env-comment': toggleSystemEnvComment,
  'cancel-system-env': cancelSystemEnvEdit,
  'save-system-env': saveSystemEnv,
  'apply-system-env': applySystemEnv,
  'reset-conversation-memory': resetConversationMemory,
  'edit-manual-config': enterManualConfigEdit,
  'cancel-manual-config': cancelManualConfigEdit,
  'save-manual-config': () => saveManualConfig().then(async function(saved) {
    if (!saved) return;
    await loadConfig();
    applyVoiceModeVisibility();
    await loadManualConfig(true);
  }),
  'close-test-toast': closeTestToast,
  'close-wifi-modal': closeWifiModal,
  'connect-selected-wifi': connectSelectedWifi,
  'toggle-wifi-list': toggleWifiListExpanded,
  'add-model-provider': () => ModelProvidersManager.addRecord(),
  'add-tts-provider': () => TtsProvidersManager.addRecord(),
  'add-stt-provider': () => SttProvidersManager.addRecord(),
  'add-voice-model-provider': () => VoiceModelProvidersManager.addRecord(),
  'enter-edit-voice-mode': () => enterEditSectionFields('voice-mode', 'agent', ['input_mode'], 'section-voice_mode'),
  'cancel-edit-voice-mode': () => {
    cancelEditSectionFields('voice-mode');
    applyVoiceModeVisibility();
  },
  'save-voice-mode': () => saveSectionFields('voice-mode', 'agent', ['input_mode'], 'section-voice_mode', 'save-voice_mode')
    .finally(applyVoiceModeVisibility),
  'enter-edit-realtime-mode': () => enterEditSection('voice_model', 'realtime-mode-group'),
  'cancel-edit-realtime-mode': () => cancelEditSection('voice_model', 'realtime-mode-group'),
  'save-realtime-mode': () => saveSection('voice_model', 'realtime-mode-group'),
  'enter-edit-classic-mode': () => enterClassicModeEdit(),
  'cancel-edit-classic-mode': () => cancelClassicModeEdit(),
  'save-classic-mode': () => saveClassicMode(),
  'enter-edit-memory-settings': () => enterMemorySettingsEdit(),
  'cancel-edit-memory-settings': () => cancelMemorySettingsEdit(),
  'save-memory-settings': () => saveMemorySettings()
};

document.addEventListener('click', function(event) {
  const target = event.target.closest('[data-action]');
  if (!target) return;
  const action = target.dataset.action;
  if (simpleActions[action]) {
    simpleActions[action]();
    return;
  }
  const section = target.dataset.sectionTarget;
  if (action === 'enter-edit-section') {
    if (section === 'device') {
      enterEditSection('device');
      enterEditSectionFields('device-hid', 'hid', ['keyboard_layout'], 'section-device');
    } else if (section === 'agent') {
      enterEditSectionFields('agent-conversation', 'agent', agentConversationFieldKeys(), 'section-agent');
    } else if (section === 'log') {
      enterEditSection('log', 'log-settings-group');
      enterEditSectionFields('log-model', 'model', ['log_raw_http'], 'log-settings-group');
    } else enterEditSection(section);
  } else if (action === 'cancel-edit-section') {
    if (section === 'device') {
      cancelEditSection('device');
      cancelEditSectionFields('device-hid');
    } else if (section === 'agent') {
      cancelEditSectionFields('agent-conversation');
    } else if (section === 'log') {
      cancelEditSection('log', 'log-settings-group');
      cancelEditSectionFields('log-model');
    } else cancelEditSection(section);
  } else if (action === 'test-section') {
    if (section === 'device') testSection(['device', 'hid']);
    else testSection(section);
  } else if (action === 'save-section') {
    if (section === 'device') saveSection('device').then((saved) => { if (saved) return saveSectionFields('device-hid', 'hid', ['keyboard_layout'], 'section-device', 'save-device'); });
    else if (section === 'agent') saveSectionFields('agent-conversation', 'agent', agentConversationFieldKeys(), 'section-agent', 'save-agent');
    else if (section === 'log') saveLogSection();
    else saveSection(section);
  }
  else if (action === 'edit-selected-provider') editSelectedProvider(target.dataset.providerKind);
  else if (action === 'delete-selected-provider') deleteSelectedProvider(target.dataset.providerKind);
  else if (action === 'open-wifi-modal') openWifiModal(target.dataset.ssid);
  else if (action === 'connect-saved-wifi') connectSavedWifi(target.dataset.ssid);
  else if (action === 'forget-wifi') {
    event.stopPropagation();
    forgetWifi(target.dataset.ssid);
  } else {
    handleProviderAction(action, target);
  }
});
document.addEventListener('change', function(event) {
  if (event.target.dataset.action === 'save-locale') saveLocale(event.target.value);
  else if (event.target.dataset.action === 'save-timezone') saveTimezone(event.target.value);
  else if (event.target.dataset.action === 'wifi-proxy-mode') syncWifiProxyFields();
  else if (event.target.id === 'agent_input_mode') applyVoiceModeVisibility();
  else if (event.target.id === 'configBackupInput') {
    restoreConfigBackup(event.target).then(async function(restored) {
      if (!restored) return;
      await loadConfig();
      applyVoiceModeVisibility();
      await loadManualConfig(true);
      await refreshStorage(false);
    });
  }
});

async function saveTimezone(timezone) {
  const select = byId('agent_timezone');
  const previous = appState.config?.agent?.timezone || 'UTC';
  if (select) select.disabled = true;
  try {
    const payload = await request('/api/config', {
      method: 'PATCH',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({config: {agent: {timezone}}})
    });
    if (payload.config) appState.config = payload.config;
    else {
      appState.config = appState.config || {};
      appState.config.agent = appState.config.agent || {};
      appState.config.agent.timezone = timezone;
    }
    setBanner(t(payload.pending ? 'timezone.saved_pending' : 'timezone.saved'), false);
    setDetails('');
  } catch (err) {
    const persisted = err && err.persisted === true;
    if (persisted) {
      if (err.config) appState.config = err.config;
      else {
        appState.config = appState.config || {};
        appState.config.agent = appState.config.agent || {};
        appState.config.agent.timezone = timezone;
      }
    } else if (select) select.value = previous;
    setBanner(t(persisted && err.applied === false ? 'timezone.saved_not_applied' : 'timezone.save_failed'), true);
    setDetails(err.message);
  } finally {
    if (select) select.disabled = false;
  }
}
window.addEventListener('click', function(event) {
  if (event.target === byId('wifiModal')) closeWifiModal();
});
document.addEventListener('selectionchange', applyPendingAgentLogSnapshotIfIdle);
const systemEnvEditor = byId('system_env_content');
if (systemEnvEditor) systemEnvEditor.addEventListener('keydown', handleSystemEnvEditorKeydown);
const agentLogText = byId('agentLogText');
if (agentLogText) agentLogText.addEventListener('scroll', syncAgentLogAutoScroll);
setAgentLogAutoScroll(true);

async function reloadAll() {
  byId('reloadBtn').disabled = true;
  try {
    await loadConfig();
    await loadManualConfig(true);
    await scanWifi(false);
    setBanner(t('page.config_refreshed'), false);
  } catch (err) {
    setBanner(t('page.refresh_failed'), true);
    setDetails(err.message);
  } finally {
    byId('reloadBtn').disabled = false;
  }
}

async function init() {
  initI18n();
  initBackup();
  initProviders();
  configureTerminalLink();
  let metaOk = true;
  setBanner(t('page.reading_config_metadata'), false);
  try {
    await loadConfigMeta();
  } catch (err) {
    metaOk = false;
    setDetails(err.message);
  }
  if (metaOk) {
    hydrateSelectOptions();
    bindFieldVisibility();
    applyMindmapFieldVisibility();
    partitionAgentFields();
    moveHidGroupedFields();
    moveVoiceModeField();
    moveModelLogField();
    moveConversationWebSearch();
    annotateConfigGroups();
    // Product groups are defined in index.html; no runtime section reparenting.
  }
  lockAllSections();
  setBanner(t('page.reading_config'), false);
  try {
    await loadConfig();
    applyVoiceModeVisibility();
    setManualConfigLocked(true);
    await loadManualConfig(false);
    await scanWifi(false);
    await refreshAgentLog(false);
    setBanner(initialReadyMessage(metaOk), !metaOk);
  } catch (err) {
    setBanner(t('page.initialization_failed'), true);
    setDetails(err.message);
  }
  if (!metaOk) disableAgentConfigEditing();
  refreshSystemEnvApplication();
  setInterval(refreshSystemEnvApplication, 3000);
  refreshConfigApplication();
  setInterval(refreshConfigApplication, 3000);
  refreshStorage(false);
  setInterval(() => refreshAgentStatus(false), 5000);
  setInterval(() => refreshAgentLog(false), 2000);
  setInterval(() => refreshOtaLog(false), 2000);
  setInterval(() => refreshStorage(false), 3000);
}

function applyMindmapFieldVisibility() {
  document.querySelectorAll('[data-config-field]').forEach(field => {
    const path = field.dataset.configField;
    if (!VISIBLE_FIELDS[path]) field.classList.add('hidden');
  });
  document.querySelectorAll('.section-card[id^="section-"]').forEach(card => {
    if (card.id === 'section-system_env') return;
    const fields = card.querySelectorAll('[data-config-field]');
    if (!fields.length) return;
    card.classList.toggle('hidden', !Array.from(fields).some(field => !field.classList.contains('hidden')));
  });
}

function moveHidGroupedFields() {
  const field = byId('hid_keyboard_layout')?.closest('.field');
  const target = byId('device-hid-fields');
  if (field && target && field.parentNode !== target) target.appendChild(field);
}

function partitionAgentFields() {
  const staging = document.querySelector('.agent-field-staging[data-config-section="agent"]');
  if (!staging) return;
  const targets = Array.from(document.querySelectorAll('[data-agent-fields-target]'));
  const extra = targets.find((target) => target.dataset.agentFieldsTarget === 'extra');
  Array.from(staging.querySelectorAll(':scope > .field')).forEach((field) => {
    const path = field.dataset.configField || '';
    const key = path.slice(path.indexOf('.') + 1);
    const target = targets.find((candidate) => {
      const keys = candidate.dataset.agentFieldsTarget.split(/\s+/).filter(Boolean);
      return keys.includes(key);
    }) || extra;
    if (target) target.appendChild(field);
  });
  if (extra && !extra.querySelector('.field')) extra.closest('.config-subsection')?.remove();
}

function agentConversationFieldKeys() {
  const card = byId('section-agent');
  if (!card) return [];
  return Array.from(card.querySelectorAll('[data-config-field^="agent."]'))
    .filter((field) => !field.classList.contains('hidden'))
    .map((field) => field.dataset.configField.slice('agent.'.length));
}

function moveVoiceModeField() {
  const field = byId('agent_input_mode')?.closest('.field');
  const target = byId('voice-mode-fields');
  if (field && target && field.parentNode !== target) target.appendChild(field);
}

function moveModelLogField() {
  const field = byId('model_log_raw_http')?.closest('.field');
  const target = byId('log-model-fields');
  if (field && target && field.parentNode !== target) target.appendChild(field);
}

function applyVoiceModeVisibility() {
  const mode = byId('agent_input_mode')?.value || '';
  const realtime = byId('realtime-mode-group');
  const classic = byId('classic-mode-group');
  if (realtime) realtime.hidden = mode !== 'realtime';
  if (classic) classic.hidden = mode !== 'stt';
}

function enterClassicModeEdit() {
  enterEditSection('stt', 'classic-mode-group');
  enterEditSection('tts', 'classic-mode-group');
}

function cancelClassicModeEdit() {
  cancelEditSection('stt', 'classic-mode-group');
  cancelEditSection('tts', 'classic-mode-group');
}

async function saveClassicMode() {
  await saveSections(['stt', 'tts'], 'classic-mode-group', 'save-classic-mode');
}

function enterMemorySettingsEdit() {
  enterEditSection('quick_capture', 'memory-settings-card');
  enterEditSection('voice_notifications', 'memory-settings-card');
}

function cancelMemorySettingsEdit() {
  cancelEditSection('quick_capture', 'memory-settings-card');
  cancelEditSection('voice_notifications', 'memory-settings-card');
}

async function saveMemorySettings() {
  await saveSections(
    ['quick_capture', 'voice_notifications'],
    'memory-settings-card',
    'save-memory-settings'
  );
}

async function saveLogSection() {
  await saveFieldGroups([
    {section: 'log'},
    {section: 'model', keys: ['log_raw_http'], scope: 'log-model'}
  ], 'log-settings-group', 'save-log');
}

function moveConversationWebSearch() {
  const card = byId('section-search');
  const target = byId('conversation-websearch');
  if (card && target && card.parentNode !== target) target.appendChild(card);
}

function annotateConfigGroups() {
  Object.entries(SECTION_TO_GROUP_MAP).forEach(([section, groups]) => {
    const card = byId('section-' + section);
    if (!card) return;
    card.dataset.configGroups = (Array.isArray(groups) ? groups : [groups]).join(',');
  });
}

init();
