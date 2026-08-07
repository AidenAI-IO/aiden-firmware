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
    'wifi.scan_refreshed': 'Wi-Fi list refreshed.',
    'wifi.scan_failed': 'Failed to scan Wi-Fi.',
    'wifi.connect_failed': 'Failed to connect to Wi-Fi.',
    'wifi.connect_not_confirmed': 'Connection not confirmed to target network.',
    'wifi.forget_failed': 'Failed to forget Wi-Fi.',
    'wifi.forget_apply_failed_detail': 'Network removed from saved list, but runtime config update failed.',
    'status.title': 'Agent Status',
    'status.process': 'Process',
    'status.port': 'Port',
    'status.loading': 'Loading',
    'status.load_failed': 'Load Failed',
    'status.state.running': 'Running',
    'status.state.starting': 'Starting',
    'status.state.error': 'Startup Failed',
    'status.state.stopped': 'Stopped',
    'status.state.port_unreachable': 'Port Unreachable',
    'status.state.unknown': 'Unknown',
    'status.watchdog_pid': 'watchdog PID {{pid}}',
    'status.watchdog_stopped': 'watchdog stopped',
    'status.reachable': 'Reachable',
    'status.unreachable': 'Unreachable',
    'status.recent_startup_log': 'Recent startup log:\n{{log}}',
    'status.refreshed': 'Agent status refreshed.',
    'status.refresh_failed': 'Failed to refresh Agent status.',
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
    'config.default_empty': 'Default: Empty',
    'config.default_value': 'Default: {{value}}',
    'config.fields.device.device_type.help': 'Android uses HID touchscreen mode. iOS, macOS, windows, and linux use absolute pointer mode.',
    'config.fields.model.reasoning_effort.help': 'Empty = auto (disable reasoning only for no-tool requests). Levels are provider-specific: minimal is OpenRouter and Volcengine Ark only, none is not supported by Ark.',
    'config.fields.model.context_window.placeholder': '0 = auto',
    'config.fields.model.context_window.help': '0 = auto: use provider metadata when available.',
    'config.fields.model.model_max_output_tokens.placeholder': '0 = auto',
    'config.fields.model.model_max_output_tokens.help': '0 = auto: use provider metadata when available.',
    'config.fields.audio_archive.enabled.help': 'After enabling, save STT voice recording WAV for Web UI playback; Automatically delete old files when exceeding quantity or capacity limit.',
    'config.fields.ota.github_proxy_url.label': 'GitHub Proxy URL',
    'config.fields.ota.github_proxy_url.placeholder': 'Leave empty to disable',
    'config.fields.ota.github_proxy_url.help': 'Optional proxy to accelerate GitHub downloads (e.g., https://gh-proxy.com/ or https://ghfast.top/)',
    'config.fields.hid.keyboard_layout.help': 'How the phone interprets the USB keyboard. Keep qwerty unless typed text comes out transposed; then switch the phone input language to match, save, and reboot the board.',
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
    'provider.type_required': 'Provider type is required',
    'provider.in_use': 'Provider is in use. Add or select another provider before deleting it.',
    'provider.delete_in_use_confirm': 'This provider is in use. Delete it and switch to another configured provider?',
    'provider.type': 'Provider',
    'provider.type_short': 'Type',
    'provider.api_key': 'API Key',
    'provider.base_url': 'Base URL',
    'provider.base_url_optional': 'Base URL (optional)',
    'provider.name': 'Name',
    'provider.select': '-- Select --',
    'provider.api_key_placeholder': 'sk-... or $OPENAI_API_KEY',
    'provider.api_key_help': 'Starts with $ = read from that environment variable.',
    'provider.name_help_model': 'Identifies this entry in the model provider list. Auto-filled; edit to override.',
    'provider.name_help': 'Identifies this entry in the provider list. Auto-filled; edit to override.',
    'provider.save_changes': 'Save Changes',
    'provider.auto': 'auto',
    'ota.update_request_sent': 'OTA update request sent, waiting for server confirmation...',
    'ota.update_started_waiting': 'OTA update started, waiting for log output...',
    'ota.update_started': 'OTA update started.',
    'ota.update_triggered': 'OTA update triggered.',
    'ota.update_failed': 'OTA update failed (rc={{rc}}).',
    'ota.recent_log': 'Recent OTA log:\n{{log}}',
    'ota.waiting_output': 'Waiting for OTA log output',
    'ota.started_waiting_output': 'OTA update started, waiting for log output',
    'ota.log_empty': 'Log is empty',
    'ota.log_unreadable': 'Log file is not readable yet',
    'ota.showing_latest': 'Showing latest segment',
    'ota.log_refreshed': 'OTA log refreshed.',
    'ota.log_refresh_failed': 'Failed to refresh OTA log.',
    'ota.log_read_failed': 'Failed to read OTA log:\n{{error}}',
    'ota.trigger_failed': 'Failed to trigger OTA update.',
    'ota.trigger_failed_detail': 'Failed to trigger OTA update:\n{{error}}',
    'ota.health.success': 'success',
    'ota.health.failed': 'failed',
    'ota.health.pending': 'pending confirmation',
    'ota.health.rolled_back': 'rolled back',
    'ota.firmware_version': 'Firmware version: {{version}}',
    'ota.health_status': 'OTA status: {{status}}',
    'ota.phase': 'phase: {{phase}}',
    'ota.slot': 'slot {{slot}}',
    'ota.error': 'error: {{error}}',
    'ota.previous_version': 'previous stable version: {{version}}',
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
    'wifi.scan_refreshed': 'Wi-Fi 列表已刷新。',
    'wifi.scan_failed': '扫描 Wi-Fi 失败。',
    'wifi.connect_failed': '连接 Wi-Fi 失败。',
    'wifi.connect_not_confirmed': '未确认已连接到目标网络。',
    'wifi.forget_failed': '忘记 Wi-Fi 失败。',
    'wifi.forget_apply_failed_detail': '网络已从保存列表移除，但运行时配置更新失败。',
    'status.title': 'Agent 状态',
    'status.process': '进程',
    'status.port': '端口',
    'status.loading': '加载中',
    'status.load_failed': '加载失败',
    'status.state.running': '运行中',
    'status.state.starting': '启动中',
    'status.state.error': '启动失败',
    'status.state.stopped': '已停止',
    'status.state.port_unreachable': '端口不可达',
    'status.state.unknown': '未知',
    'status.watchdog_pid': 'watchdog PID {{pid}}',
    'status.watchdog_stopped': 'watchdog 已停止',
    'status.reachable': '可达',
    'status.unreachable': '不可达',
    'status.recent_startup_log': '最近启动日志：\n{{log}}',
    'status.refreshed': 'Agent 状态已刷新。',
    'status.refresh_failed': '刷新 Agent 状态失败。',
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
    'config.default_empty': '默认值：空',
    'config.default_value': '默认值：{{value}}',
    'config.fields.device.device_type.help': 'Android 使用 HID touchscreen 模式。iOS、macOS、windows 和 linux 使用 absolute 指针模式。',
    'config.fields.model.reasoning_effort.help': '留空 = 自动（仅无工具请求禁用推理）；档位与提供商相关：minimal 支持 OpenRouter 和火山方舟，方舟不支持 none。',
    'config.fields.model.context_window.placeholder': '0 = 自动',
    'config.fields.model.context_window.help': '0 = 自动：可用时使用提供商元数据。',
    'config.fields.model.model_max_output_tokens.placeholder': '0 = 自动',
    'config.fields.model.model_max_output_tokens.help': '0 = 自动：可用时使用提供商元数据。',
    'config.fields.audio_archive.enabled.help': '启用后保存 STT 语音录音 WAV，供 Web UI 回放；超过数量或容量限制时自动删除旧文件。',
    'config.fields.ota.github_proxy_url.label': 'GitHub 代理 URL',
    'config.fields.ota.github_proxy_url.placeholder': '留空则禁用',
    'config.fields.ota.github_proxy_url.help': '用于加速 GitHub 下载的可选代理（例如 https://gh-proxy.com/ 或 https://ghfast.top/）',
    'config.fields.hid.keyboard_layout.help': '手机如何解释 USB 键盘。除非输入的文本出现错位，否则保持 qwerty；如出现错位，请先将手机输入语言切换为匹配的语言，再保存并重启板子。',
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
    'provider.type_required': '提供商类型为必填项',
    'provider.in_use': '此提供商正在使用中。请先新增或选择另一个提供商。',
    'provider.delete_in_use_confirm': '此提供商正在使用中。删除并切换到另一个已配置的提供商吗？',
    'provider.type': '提供商',
    'provider.type_short': '类型',
    'provider.api_key': 'API 密钥',
    'provider.base_url': 'Base URL',
    'provider.base_url_optional': 'Base URL（可选）',
    'provider.name': '名称',
    'provider.select': '-- 选择 --',
    'provider.api_key_placeholder': 'sk-... 或 $OPENAI_API_KEY',
    'provider.api_key_help': '以 $ 开头表示从该环境变量读取。',
    'provider.name_help_model': '用于在模型提供商列表中标识此项。自动填充，可自行修改。',
    'provider.name_help': '用于在提供商列表中标识此项。自动填充，可自行修改。',
    'provider.save_changes': '保存更改',
    'provider.auto': '自动',
    'ota.update_request_sent': 'OTA 更新请求已发送，正在等待服务器确认…',
    'ota.update_started_waiting': 'OTA 更新已开始，正在等待日志输出…',
    'ota.update_started': 'OTA 更新已开始。',
    'ota.update_triggered': '已触发 OTA 更新。',
    'ota.update_failed': 'OTA 更新失败（rc={{rc}}）。',
    'ota.recent_log': '最近 OTA 日志：\n{{log}}',
    'ota.waiting_output': '等待 OTA 日志输出',
    'ota.started_waiting_output': 'OTA 更新已开始，正在等待日志输出',
    'ota.log_empty': '日志为空',
    'ota.log_unreadable': '日志文件暂时无法读取',
    'ota.showing_latest': '显示最新片段',
    'ota.log_refreshed': 'OTA 日志已刷新。',
    'ota.log_refresh_failed': '刷新 OTA 日志失败。',
    'ota.log_read_failed': '读取 OTA 日志失败：\n{{error}}',
    'ota.trigger_failed': '触发 OTA 更新失败。',
    'ota.trigger_failed_detail': '触发 OTA 更新失败：\n{{error}}',
    'ota.health.success': '成功',
    'ota.health.failed': '失败',
    'ota.health.pending': '等待确认',
    'ota.health.rolled_back': '已回滚',
    'ota.firmware_version': '固件版本：{{version}}',
    'ota.health_status': 'OTA 状态：{{status}}',
    'ota.phase': '阶段：{{phase}}',
    'ota.slot': '槽位 {{slot}}',
    'ota.error': '错误：{{error}}',
    'ota.previous_version': '上一稳定版本：{{version}}',
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
