import {setBanner, setDetails} from './api.js';
import {bindFieldVisibility, hydrateSelectOptions} from './config-meta.js';
import {
  cancelEditSection, closeTestToast, disableAgentConfigEditing, enterEditSection,
  initialReadyMessage, loadConfig, loadConfigMeta, lockAllSections, saveSection, testSection
} from './config-form.js';
import {initI18n, saveLocale, t} from './i18n.js';
import {applyPendingAgentLogSnapshotIfIdle, exportLogs, refreshAgentLog, setAgentLogAutoScroll, syncAgentLogAutoScroll, toggleAgentLogAutoScroll} from './logs.js';
import {refreshAgentStatus} from './agent-status.js';
import {refreshOtaLog, triggerOtaUpdate} from './ota.js';
import {
  deleteSelectedProvider, editSelectedProvider, handleProviderAction, initProviders,
  ModelProvidersManager, SttProvidersManager, TtsProvidersManager
} from './providers.js';
import {byId, configureTerminalLink} from './state.js';
import {ejectStorageCard, refreshStorage, startStorageFormat} from './storage.js';
import {toggleSTTTest} from './stt-test.js';
import {cancelSystemEnvEdit, enterSystemEnvEdit, handleSystemEnvEditorKeydown, saveSystemEnv, toggleSystemEnvComment} from './system-env.js';
import {closeWifiModal, connectSavedWifi, connectSelectedWifi, forgetWifi, openWifiModal, scanWifi, toggleWifiListExpanded} from './wifi.js';

const simpleActions = {
  'export-logs': exportLogs,
  'ota-update': triggerOtaUpdate,
  'refresh-ota-log': () => refreshOtaLog(true),
  'reload-all': reloadAll,
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
  'close-test-toast': closeTestToast,
  'close-wifi-modal': closeWifiModal,
  'connect-selected-wifi': connectSelectedWifi,
  'toggle-wifi-list': toggleWifiListExpanded,
  'add-model-provider': () => ModelProvidersManager.addProvider(),
  'add-tts-provider': () => TtsProvidersManager.addRecord(),
  'add-stt-provider': () => SttProvidersManager.addRecord()
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
  if (action === 'enter-edit-section') enterEditSection(section);
  else if (action === 'cancel-edit-section') cancelEditSection(section);
  else if (action === 'test-section') testSection(section);
  else if (action === 'save-section') saveSection(section);
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
  refreshStorage(false);
  setInterval(() => refreshAgentStatus(false), 5000);
  setInterval(() => refreshAgentLog(false), 2000);
  setInterval(() => refreshOtaLog(false), 2000);
  setInterval(() => refreshStorage(false), 3000);
}

init();
