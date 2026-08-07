import {appState, byId, registerRuntime, runtimeFunction} from './state.js';

const request = runtimeFunction('request');
const setBanner = runtimeFunction('setBanner');
const setDetails = runtimeFunction('setDetails');
const loadAuthoritativeLocale = runtimeFunction('loadAuthoritativeLocale');
const LOCALE_STORAGE_KEY = 'aiden.config.locale';

const messages = {
  'en-US': {
    'document.title': 'Aiden Setup',
    'page.title': 'Configuration',
    'page.language': 'Language',
    'page.llm_logs': 'LLM Logs',
    'page.terminal': 'Terminal',
    'page.export_logs': 'Export Logs',
    'page.ota_update': 'OTA Update',
    'action.ready': 'Ready.',
    'action.refresh_log': 'Refresh Log',
    'action.refresh_page': 'Refresh Page Data',
    'action.rescan': 'Rescan',
    'action.refresh_status': 'Refresh Status',
    'action.auto_scroll': 'Auto-scroll',
    'action.edit': 'Edit',
    'action.test': 'Test',
    'action.cancel': 'Cancel',
    'action.save': 'Save',
    'action.add': 'Add',
    'action.delete': 'Delete',
    'action.connect': 'Connect',
    'action.forget': 'Forget',
    'action.close': 'Close',
    'action.toggle_comment': 'Toggle #',
    'ota.live_log': 'OTA Live Log',
    'ota.waiting': 'Waiting for update',
    'ota.click_to_read': 'Click OTA Update to read logs',
    'wifi.title': 'Wi-Fi Configuration',
    'wifi.connect_title': 'Connect to Wi-Fi',
    'wifi.selected_network': 'Selected network',
    'wifi.password': 'Password',
    'wifi.password_optional': 'Open network can leave empty',
    'wifi.no_networks': 'No networks scanned',
    'wifi.rescan_hint': 'Click "Rescan" to try again',
    'wifi.hide_others': 'Hide other Wi-Fi',
    'wifi.show_others': 'Show other Wi-Fi ({{count}})',
    'wifi.connected': 'Connected',
    'wifi.connected_ip': 'Connected · {{ip}}',
    'wifi.saved_top': 'Saved · highest priority',
    'wifi.saved_priority': 'Saved · priority {{priority}}',
    'wifi.connect_to': 'Connect to "{{ssid}}"',
    'wifi.connecting_to': 'Connecting to "{{ssid}}"...',
    'wifi.connect_failed_detail': 'Failed to connect to "{{ssid}}", please check password and try again.',
    'wifi.connected_to': 'Connected to "{{ssid}}".',
    'wifi.forget_confirm': 'Forget "{{ssid}}"?',
    'wifi.forgot': 'Forgot "{{ssid}}".',
    'wifi.forgot_apply_failed': 'Forgot "{{ssid}}", but runtime apply failed.',
    'wifi.connecting': 'Connecting',
    'status.title': 'Agent Status',
    'status.process': 'Process',
    'status.port': 'Port',
    'status.loading': 'Loading',
    'logs.live_agent': 'Live Agent Log',
    'config.title': 'Agent Configuration',
    'config.save_failed': 'Save [{{section}}] failed.',
    'config.saved': '[{{section}}] saved.',
    'config.editing': 'Editing [{{section}}], click Save after changes.',
    'config.cancelled': 'Cancelled changes to [{{section}}].',
    'config.stt_only': '[{{section}}] can only be {{action}} when agent.input_mode = stt.',
    'config.stt_edit_only': '[{{section}}] can only be edited when agent.input_mode = stt.',
    'config.stt_save_only': '[{{section}}] can only be saved when agent.input_mode = stt.',
    'config.stt_test_only': '[{{section}}] can only be tested when agent.input_mode = stt.',
    'config.switch_to_stt': 'First change input_mode to stt in [agent] and save.',
    'config.testing': 'Testing [{{section}}]',
    'config.test_passed': '[{{section}}] test passed',
    'config.test_failed': '[{{section}}] test not passed',
    'config.test_request_failed': 'Test [{{section}}] failed',
    'config.audio_archive_available': 'In STT mode, saves voice recordings after wake-up for history playback.',
    'config.audio_archive_locked': 'Only effective when agent.input_mode = stt; switch to STT and Edit recording Save settings.',
    'config.stt_test_help': 'Click Test will activate microphone to start recording, click again to end recording and display recognition result.',
    'locale.saved': 'Language saved. Agent is restarting.',
    'locale.save_failed': 'Failed to save language.',
    'provider.add': 'Add Provider',
    'provider.edit': 'Edit Provider',
    'provider.none': 'No providers configured yet.',
    'provider.none_help': 'Click "Add Provider" to create one.',
    'provider.required': 'Provider is required',
    'provider.name_required': 'Name is required',
    'provider.env_required': 'Environment variable name is required after $',
    'provider.delete_confirm': 'Delete provider "{{name}}"?',
    'provider.exists': 'Provider "{{name}}" already exists',
    'provider.saved': 'Providers saved.',
    'provider.save_failed': 'Failed to save providers.',
    'provider.choose_model': 'Choose a model',
    'provider.current_model': 'Current model',
    'provider.loading_models': 'Loading models...',
    'provider.load_models_failed': 'Failed to load models',
    'provider.no_models': 'No models available for this provider.',
    'provider.offline_models': 'Model list needs a running agent. Enter the model ID manually.',
    'provider.custom_model': 'Custom Model ID:',
    'provider.model_placeholder': 'Enter model name',
    'provider.recommended': 'Recommended',
    'stt.starting': 'Starting...',
    'stt.ending': 'Ending...',
    'stt.end_recording': 'End Recording',
    'stt.activating': '[stt] Activating microphone',
    'stt.recording': '[stt] Recording',
    'stt.recognizing': '[stt] Recognizing',
    'stt.completed': '[stt] Recognition completed',
    'stt.failed': '[stt] test failed',
    'stt.start_failed': 'Failed to start [stt] test',
    'stt.stop_failed': 'Failed to end [stt] test',
    'stt.start_speaking': 'Please start speaking, click "End Recording" again to submit for recognition.',
    'stt.keep_speaking': 'Please speak, click Test button again after ending.',
    'stt.waiting_result': 'Recording ended, waiting for recognition result...',
    'stt.result': 'Recognition result:',
    'stt.no_result': 'No recognition result returned',
    'common.unknown_error': 'unknown error',
  },
  'zh-CN': {
    'document.title': 'Aiden 设置',
    'page.title': '配置',
    'page.language': '语言',
    'page.llm_logs': 'LLM 日志',
    'page.terminal': '终端',
    'page.export_logs': '导出日志',
    'page.ota_update': 'OTA 更新',
    'action.ready': '就绪。',
    'action.refresh_log': '刷新日志',
    'action.refresh_page': '刷新页面数据',
    'action.rescan': '重新扫描',
    'action.refresh_status': '刷新状态',
    'action.auto_scroll': '自动滚动',
    'action.edit': '编辑',
    'action.test': '测试',
    'action.cancel': '取消',
    'action.save': '保存',
    'action.add': '新增',
    'action.delete': '删除',
    'action.connect': '连接',
    'action.forget': '忘记',
    'action.close': '关闭',
    'action.toggle_comment': '切换注释 #',
    'ota.live_log': 'OTA 实时日志',
    'ota.waiting': '等待更新',
    'ota.click_to_read': '点击 OTA 更新后查看日志',
    'wifi.title': 'Wi-Fi 配置',
    'wifi.connect_title': '连接 Wi-Fi',
    'wifi.selected_network': '已选择网络',
    'wifi.password': '密码',
    'wifi.password_optional': '开放网络可留空',
    'wifi.no_networks': '未扫描到网络',
    'wifi.rescan_hint': '点击“重新扫描”重试',
    'wifi.hide_others': '隐藏其他 Wi-Fi',
    'wifi.show_others': '显示其他 Wi-Fi（{{count}}）',
    'wifi.connected': '已连接',
    'wifi.connected_ip': '已连接 · {{ip}}',
    'wifi.saved_top': '已保存 · 最高优先级',
    'wifi.saved_priority': '已保存 · 优先级 {{priority}}',
    'wifi.connect_to': '连接到“{{ssid}}”',
    'wifi.connecting_to': '正在连接“{{ssid}}”…',
    'wifi.connect_failed_detail': '连接“{{ssid}}”失败，请检查密码后重试。',
    'wifi.connected_to': '已连接到“{{ssid}}”。',
    'wifi.forget_confirm': '忘记“{{ssid}}”？',
    'wifi.forgot': '已忘记“{{ssid}}”。',
    'wifi.forgot_apply_failed': '已忘记“{{ssid}}”，但运行时应用失败。',
    'wifi.connecting': '连接中',
    'status.title': 'Agent 状态',
    'status.process': '进程',
    'status.port': '端口',
    'status.loading': '加载中',
    'logs.live_agent': 'Agent 实时日志',
    'config.title': 'Agent 配置',
    'config.save_failed': '保存 [{{section}}] 失败。',
    'config.saved': '[{{section}}] 已保存。',
    'config.editing': '正在编辑 [{{section}}]，修改后请点击保存。',
    'config.cancelled': '已取消 [{{section}}] 的修改。',
    'config.stt_only': '[{{section}}] 仅在 agent.input_mode = stt 时可{{action}}。',
    'config.stt_edit_only': '[{{section}}] 仅在 agent.input_mode = stt 时可编辑。',
    'config.stt_save_only': '[{{section}}] 仅在 agent.input_mode = stt 时可保存。',
    'config.stt_test_only': '[{{section}}] 仅在 agent.input_mode = stt 时可测试。',
    'config.switch_to_stt': '请先在 [agent] 中将 input_mode 改为 stt 并保存。',
    'config.testing': '正在测试 [{{section}}]',
    'config.test_passed': '[{{section}}] 测试通过',
    'config.test_failed': '[{{section}}] 测试未通过',
    'config.test_request_failed': '测试 [{{section}}] 失败',
    'config.audio_archive_available': '在 STT 模式下，唤醒后的语音录音会保存以供历史回放。',
    'config.audio_archive_locked': '仅在 agent.input_mode = stt 时生效；请切换到 STT 后编辑并保存录音设置。',
    'config.stt_test_help': '点击测试会启动麦克风录音，再次点击可结束录音并显示识别结果。',
    'locale.saved': '语言已保存，Agent 正在重启。',
    'locale.save_failed': '保存语言失败。',
    'provider.add': '添加提供商',
    'provider.edit': '编辑提供商',
    'provider.none': '尚未配置任何提供商。',
    'provider.none_help': '点击“添加提供商”创建一个。',
    'provider.required': '提供商为必填项',
    'provider.name_required': '名称为必填项',
    'provider.env_required': '$ 后面需要填写环境变量名',
    'provider.delete_confirm': '删除提供商“{{name}}”？',
    'provider.exists': '提供商“{{name}}”已存在',
    'provider.saved': '提供商已保存。',
    'provider.save_failed': '保存提供商失败。',
    'provider.choose_model': '选择模型',
    'provider.current_model': '当前模型',
    'provider.loading_models': '正在加载模型…',
    'provider.load_models_failed': '加载模型失败',
    'provider.no_models': '此提供商没有可用模型。',
    'provider.offline_models': '模型列表需要 Agent 正在运行。请手动输入模型 ID。',
    'provider.custom_model': '自定义模型 ID：',
    'provider.model_placeholder': '输入模型名称',
    'provider.recommended': '推荐',
    'stt.starting': '正在启动…',
    'stt.ending': '正在结束…',
    'stt.end_recording': '结束录音',
    'stt.activating': '[stt] 正在启用麦克风',
    'stt.recording': '[stt] 录音中',
    'stt.recognizing': '[stt] 识别中',
    'stt.completed': '[stt] 识别完成',
    'stt.failed': '[stt] 测试失败',
    'stt.start_failed': '启动 [stt] 测试失败',
    'stt.stop_failed': '结束 [stt] 测试失败',
    'stt.start_speaking': '请开始说话，再次点击“结束录音”提交识别。',
    'stt.keep_speaking': '请说话，结束后再次点击测试按钮。',
    'stt.waiting_result': '录音已结束，正在等待识别结果…',
    'stt.result': '识别结果：',
    'stt.no_result': '未返回识别结果',
    'common.unknown_error': '未知错误',
  },
};

let activeLocale = readStoredLocale();
let persistedLocale = activeLocale;
let localeRevision = 0;
let localeSaveId = 0;
let localeSavePending = false;

function normalizeLocale(locale) {
  return locale === 'en-US' ? 'en-US' : 'zh-CN';
}

function readStoredLocale() {
  try {
    return normalizeLocale(localStorage.getItem(LOCALE_STORAGE_KEY));
  } catch (_err) {
    return 'zh-CN';
  }
}

function interpolate(template, params) {
  return String(template).replace(/\{\{([A-Za-z0-9_]+)\}\}/g, (_match, name) => {
    return params[name] == null ? '' : String(params[name]);
  });
}

function t(key, params = {}) {
  const localeMessages = messages[activeLocale] || messages['en-US'];
  const fallbackMessages = messages['en-US'];
  const template = localeMessages[key] ?? fallbackMessages[key] ?? params.defaultValue ?? key;
  return interpolate(template, params);
}

function translateElements(attribute, targetAttribute) {
  document.querySelectorAll(`[${attribute}]`).forEach((element) => {
    const key = element.getAttribute(attribute);
    const defaultAttribute = `${attribute}-default`;
    const translated = t(key, {defaultValue: element.getAttribute(defaultAttribute) ?? undefined});
    if (targetAttribute === 'textContent') element.textContent = translated;
    else element.setAttribute(targetAttribute, translated);
  });
}

function translatePage() {
  document.title = t('document.title');
  translateElements('data-i18n', 'textContent');
  translateElements('data-i18n-placeholder', 'placeholder');
  translateElements('data-i18n-title', 'title');
  translateElements('data-i18n-aria-label', 'aria-label');
  if (document.dispatchEvent && typeof CustomEvent !== 'undefined') {
    document.dispatchEvent(new CustomEvent('aiden:locale-changed', {detail: {locale: activeLocale}}));
  }
}

function applyLocale(locale, remember) {
  activeLocale = normalizeLocale(locale);
  document.documentElement.lang = activeLocale;
  const selector = byId('localeSelect');
  if (selector) selector.value = activeLocale;
  if (remember) {
    persistedLocale = activeLocale;
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, activeLocale);
    } catch (_err) {
    }
  }
  translatePage();
}

async function saveLocale(locale) {
  const selector = byId('localeSelect');
  const previous = persistedLocale;
  const requested = normalizeLocale(locale);
  const saveId = ++localeSaveId;
  localeRevision++;
  localeSavePending = true;
  applyLocale(requested, false);
  if (selector) selector.disabled = true;
  try {
    const payload = await request('/api/config/locale', {
      method: 'PUT',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({locale: requested}),
    });
    if (saveId !== localeSaveId) return;
    applyLocale(payload.locale || requested, true);
    if (appState.config) {
      appState.config.agent = appState.config.agent || {};
      appState.config.agent.locale = persistedLocale;
    }
    localeSavePending = false;
    localeRevision++;
    setBanner(t('locale.saved'), false);
  } catch (err) {
    if (saveId !== localeSaveId) return;
    applyLocale(previous, false);
    if (appState.config) {
      appState.config.agent = appState.config.agent || {};
      appState.config.agent.locale = previous;
    }
    localeSavePending = false;
    localeRevision++;
    setBanner(t('locale.save_failed'), true);
    setDetails(err.message);
    try {
      await loadAuthoritativeLocale();
    } catch (_refreshErr) {
    }
  } finally {
    if (saveId === localeSaveId && selector) selector.disabled = false;
  }
}

function initI18n() {
  applyLocale(activeLocale, false);
}

function getActiveLocale() { return activeLocale; }
function getPersistedLocale() { return persistedLocale; }
function getLocaleRevision() { return localeRevision; }
function isLocaleSavePending() { return localeSavePending; }

export {
  applyLocale, getActiveLocale, getLocaleRevision, getPersistedLocale, initI18n,
  isLocaleSavePending, saveLocale, t,
};
registerRuntime({
  applyLocale, getActiveLocale, getLocaleRevision, getPersistedLocale, initI18n,
  isLocaleSavePending, saveLocale, t,
});
