import {refreshConfigApplication, retryConfigApplication} from './config-application.js';
import {setBanner, setDetails} from './api.js';
import {bindFieldVisibility, hydrateSelectOptions} from './config-meta.js';
import {
  cancelEditSection, closeTestToast, disableAgentConfigEditing, enterEditSection,
  cancelEditSectionFields, enterEditSectionFields, initialReadyMessage, loadConfig, loadConfigMeta,
  lockAllSections, rebootDevice, saveSection, saveSectionFields, testSection
} from './config-form.js';
import {initI18n, saveLocale, t} from './i18n.js';
import {applyPendingAgentLogSnapshotIfIdle, exportLogs, refreshAgentLog, setAgentLogAutoScroll, syncAgentLogAutoScroll, toggleAgentLogAutoScroll} from './logs.js';
import {refreshAgentStatus} from './agent-status.js';
import {refreshOtaLog, triggerOtaUpdate} from './ota.js';
import {
  deleteSelectedProvider, editSelectedProvider, handleProviderAction, initProviders,
  ModelProvidersManager, SttProvidersManager, TtsProvidersManager, VoiceModelProvidersManager
} from './providers.js';
import {byId, configureTerminalLink} from './state.js';
import {ejectStorageCard, refreshStorage, startStorageFormat} from './storage.js';
import {toggleSTTTest} from './stt-test.js';
import {applySystemEnv, refreshSystemEnvApplication, cancelSystemEnvEdit, enterSystemEnvEdit, handleSystemEnvEditorKeydown, saveSystemEnv, toggleSystemEnvComment} from './system-env.js';
import {closeWifiModal, connectSavedWifi, connectSelectedWifi, forgetWifi, openWifiModal, scanWifi, syncWifiProxyFields, toggleWifiListExpanded} from './wifi.js';
import {VISIBLE_FIELDS} from './field-visibility.js';
import {SECTION_TO_GROUP_MAP} from './config-groups.js';

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
  'edit-system-env': enterSystemEnvEdit,
  'toggle-system-env-comment': toggleSystemEnvComment,
  'cancel-system-env': cancelSystemEnvEdit,
  'save-system-env': saveSystemEnv,
  'apply-system-env': applySystemEnv,
  'close-test-toast': closeTestToast,
  'close-wifi-modal': closeWifiModal,
  'connect-selected-wifi': connectSelectedWifi,
  'toggle-wifi-list': toggleWifiListExpanded,
  'add-model-provider': () => ModelProvidersManager.addRecord(),
  'add-tts-provider': () => TtsProvidersManager.addRecord(),
  'add-stt-provider': () => SttProvidersManager.addRecord(),
  'add-voice-model-provider': () => VoiceModelProvidersManager.addRecord()
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
  const scope = target.dataset.sectionScope;
  if (action === 'enter-edit-section') {
    if (section === 'device') {
      enterEditSection('device');
      enterEditSectionFields('device-hid', 'hid', ['keyboard_layout'], 'section-device');
    } else if (scope === 'hid-debug') {
      enterEditSectionFields('hid-debug', 'hid', ['input_backend'], 'section-hid');
    } else enterEditSection(section);
  } else if (action === 'cancel-edit-section') {
    if (section === 'device') {
      cancelEditSection('device');
      cancelEditSectionFields('device-hid');
    } else if (scope === 'hid-debug') {
      cancelEditSectionFields('hid-debug');
    } else cancelEditSection(section);
  } else if (action === 'test-section') {
    if (section === 'device') testSection(['device', 'hid']);
    else testSection(section);
  } else if (action === 'save-section') {
    if (section === 'device') saveSection('device').then((saved) => { if (saved) return saveSectionFields('device-hid', 'hid', ['keyboard_layout'], 'section-device', 'save-device'); });
    else if (scope === 'hid-debug') saveSectionFields('hid-debug', 'hid', ['input_backend'], 'section-hid', 'save-hid');
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
  else if (event.target.dataset.action === 'wifi-proxy-mode') syncWifiProxyFields();
});
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
    moveHidGroupedFields();
    moveVoiceModeField();
    moveConversationWebSearch();
    annotateConfigGroups();
    // Product groups are defined in index.html; no runtime section reparenting.
  }
  lockAllSections();
  setBanner(t('page.reading_config'), false);
  try {
    await loadConfig();
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

function moveVoiceModeField() {
  const field = byId('agent_input_mode')?.closest('.field');
  const target = byId('voice-mode-fields');
  if (field && target && field.parentNode !== target) target.appendChild(field);
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
