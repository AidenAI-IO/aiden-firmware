import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const webRoot = path.join(repositoryRoot, 'src/config_web/web');
const i18nPath = path.join(webRoot, 'assets/js/config/i18n.js');

class Element {
  constructor(attributes = {}) {
    this.attributes = new Map(Object.entries(attributes));
    this.dataset = {};
    this.children = [];
    this.textContent = '';
    this.value = '';
    this.disabled = false;
    this._classes = new Set();
    this.className = '';
    this.scrollTop = 0;
    this.clientHeight = 0;
    this.scrollHeight = 0;
    this.classList = {
      add: (...names) => names.forEach((name) => this._classes.add(name)),
      remove: (...names) => names.forEach((name) => this._classes.delete(name)),
      contains: (name) => this._classes.has(name),
      toggle: (name, force) => {
        const enabled = force === undefined ? !this._classes.has(name) : !!force;
        if (enabled) this._classes.add(name);
        else this._classes.delete(name);
        return enabled;
      },
    };
  }

  get className() {
    return [...this._classes].join(' ');
  }

  set className(value) {
    this._classes = new Set(String(value || '').split(/\s+/).filter(Boolean));
  }

  get textContent() {
    if (this.children.length) return this.children.map((child) => child.textContent).join('');
    return this._textContent;
  }

  set textContent(value) {
    this._textContent = String(value);
    if (this.children) this.children = [];
  }

  getAttribute(name) {
    return this.attributes.has(name) ? this.attributes.get(name) : null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name === 'class') this.className = value;
    if (name.startsWith('data-')) {
      const key = name.slice(5).replace(/-([a-z])/g, (_match, letter) => letter.toUpperCase());
      this.dataset[key] = String(value);
    }
  }

  removeAttribute(name) {
    this.attributes.delete(name);
  }

  appendChild(child) {
    if (child && child.isFragment) {
      child.children.forEach((item) => { item.parentNode = this; });
      this.children.push(...child.children);
    } else {
      if (child) child.parentNode = this;
      this.children.push(child);
    }
    return child;
  }

  contains(node) {
    for (let current = node; current; current = current.parentNode) {
      if (current === this) return true;
    }
    return false;
  }

  click() {}
  remove() {}
}

const title = new Element({'data-i18n': 'page.title'});
const password = new Element({'data-i18n-placeholder': 'wifi.password_optional'});
const localeSelect = new Element();
const elements = [title, password];
const elementsById = new Map([['localeSelect', localeSelect]]);
const eventListeners = new Map();
const stored = new Map([['aiden.config.locale', 'en-US']]);
let selectionNode = null;
let fetchImpl = async () => { throw new Error('fetch is not configured'); };
const document = {
  title: '',
  documentElement: {lang: ''},
  body: new Element(),
  getElementById(id) {
    return elementsById.get(id) || null;
  },
  querySelectorAll(selector) {
    const attribute = selector.slice(1, -1);
    return elements.filter((element) => element.attributes.has(attribute));
  },
  querySelector() {
    return null;
  },
  addEventListener(type, listener) {
    const listeners = eventListeners.get(type) || [];
    listeners.push(listener);
    eventListeners.set(type, listeners);
  },
  dispatchEvent(event) {
    (eventListeners.get(event.type) || []).forEach((listener) => listener(event));
  },
  createDocumentFragment() {
    return {isFragment: true, children: [], appendChild(child) { this.children.push(child); }};
  },
  createElement() {
    return new Element();
  },
  getSelection() {
    if (!selectionNode) return {rangeCount: 0, isCollapsed: true};
    return {
      rangeCount: 1,
      isCollapsed: false,
      getRangeAt: () => ({commonAncestorContainer: selectionNode}),
    };
  },
};
class CustomEvent {
  constructor(type, options = {}) {
    this.type = type;
    this.detail = options.detail;
  }
}
const context = vm.createContext({
  CustomEvent,
  URL: {
    createObjectURL: () => 'blob:test',
    revokeObjectURL() {},
  },
  console,
  document,
  fetch: (...args) => fetchImpl(...args),
  setTimeout() {},
  localStorage: {
    getItem(key) { return stored.get(key) || null; },
    setItem(key, value) { stored.set(key, value); },
  },
});
const moduleCache = new Map();

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return {promise, resolve, reject};
}

async function loadModule(filePath) {
  const absolutePath = path.resolve(filePath);
  if (moduleCache.has(absolutePath)) return moduleCache.get(absolutePath);
  const source = await fs.readFile(absolutePath, 'utf8');
  const module = new vm.SourceTextModule(source, {
    context,
    identifier: pathToFileURL(absolutePath).href,
  });
  moduleCache.set(absolutePath, module);
  await module.link(async (specifier, referencingModule) => {
    const referencingPath = fileURLToPath(referencingModule.identifier);
    return loadModule(path.resolve(path.dirname(referencingPath), specifier));
  });
  return module;
}

const i18nModule = await loadModule(i18nPath);
await i18nModule.evaluate();
const {applyLocale, initI18n, t} = i18nModule.namespace;

assert.equal(t('config.save_failed', {section: 'agent'}), 'Save [agent] failed.');
applyLocale('zh-CN', false);
assert.equal(t('config.save_failed', {section: 'agent'}), '保存 [agent] 失败。');
assert.equal(t('wifi.connected_to', {ssid: 'Aiden Lab'}), '已连接到“Aiden Lab”。');
assert.equal(
  t('config.fields.device.device_type.help'),
  'Android 使用 HID touchscreen 模式。iOS、macOS、windows 和 linux 使用 absolute 指针模式。',
);
assert.equal(t('config.default_value', {value: '16000'}), '默认值：16000');
assert.equal(t('logs.jump_to_bottom'), '跳到底部');
assert.equal(t('system_env.saved'), 'env 已保存。');
assert.equal(t('missing.translation.key'), 'missing.translation.key');

initI18n();
assert.equal(document.documentElement.lang, 'zh-CN');
assert.equal(document.title, 'Aiden 设置');
assert.equal(title.textContent, '配置');
assert.equal(password.getAttribute('placeholder'), '开放网络可留空');

applyLocale('en-US', true);
assert.equal(document.documentElement.lang, 'en-US');
assert.equal(title.textContent, 'Configuration');
assert.equal(password.getAttribute('placeholder'), 'Open network can leave empty');
assert.equal(stored.get('aiden.config.locale'), 'en-US');

const source = await fs.readFile(i18nPath, 'utf8');
assert.doesNotMatch(source, /MutationObserver/);
assert.doesNotMatch(source, /translateDynamicText/);
assert.doesNotMatch(source, /const\s+zhText\s*=/);

const indexHtml = await fs.readFile(path.join(webRoot, 'index.html'), 'utf8');
assert.match(indexHtml, /data-i18n="page\.title"/);
assert.match(indexHtml, /data-i18n="action\.ready"/);
assert.match(indexHtml, /data-i18n-placeholder="wifi\.password_optional"/);

const configForm = await fs.readFile(path.join(webRoot, 'assets/js/config/config-form.js'), 'utf8');
const wifi = await fs.readFile(path.join(webRoot, 'assets/js/config/wifi.js'), 'utf8');
const providers = await fs.readFile(path.join(webRoot, 'assets/js/config/providers.js'), 'utf8');
const agentStatus = await fs.readFile(path.join(webRoot, 'assets/js/config/agent-status.js'), 'utf8');
const ota = await fs.readFile(path.join(webRoot, 'assets/js/config/ota.js'), 'utf8');
const sttTest = await fs.readFile(path.join(webRoot, 'assets/js/config/stt-test.js'), 'utf8');
const logs = await fs.readFile(path.join(webRoot, 'assets/js/config/logs.js'), 'utf8');
const systemEnv = await fs.readFile(path.join(webRoot, 'assets/js/config/system-env.js'), 'utf8');
const app = await fs.readFile(path.join(webRoot, 'assets/js/config/app.js'), 'utf8');
assert.match(configForm, /t\('config\.save_failed',\{section:/);
assert.match(wifi, /t\('wifi\.connected_to',\s*\{ssid\s*:/);
assert.match(wifi, /t\('wifi\.no_networks'\)/);
assert.match(wifi, /aiden:locale-changed/);
assert.doesNotMatch(wifi, /runtimeFunction\('localizedText'\)/);
assert.match(providers, /t\('provider\.choose_model'\)/);
assert.doesNotMatch(providers, /runtimeFunction\('localizedText'\)/);
assert.match(agentStatus, /t\('status\.state\.'/);
assert.match(ota, /t\('ota\.update_started'\)/);
assert.match(sttTest, /runtimeFunction\('t'\)/);
assert.match(sttTest, /'action\.test'/);
assert.match(sttTest, /aiden:locale-changed/);
assert.match(logs, /runtimeFunction\('t'\)/);
assert.match(logs, /'logs\.jump_to_bottom'/);
assert.match(logs, /aiden:locale-changed/);
assert.match(systemEnv, /runtimeFunction\('t'\)/);
assert.match(systemEnv, /t\('system_env\.saved'\)/);
assert.match(app, /t\('page\.config_refreshed'\)/);
assert.match(configForm, /t\('config\.rebooting'\)/);
assert.match(configForm, /'config\.secret_saved_placeholder'/);

const stateModule = await loadModule(path.join(webRoot, 'assets/js/config/state.js'));
await stateModule.evaluate();
const {appState, registerRuntime} = stateModule.namespace;
let latestBanner = null;
let latestDetails = null;
let requestResult = {};
let requestError = null;
let requestImpl = null;
registerRuntime({
  readSection: () => ({}),
  refreshAgentStatus: () => {},
  request: async (...args) => {
    if (requestImpl) return requestImpl(...args);
    if (requestError) throw requestError;
    return requestResult;
  },
  setBanner: (message, failed) => { latestBanner = {message, failed}; },
  setDetails: (message) => { latestDetails = message; },
});

const testButton = new Element({'data-i18n': 'action.test'});
const testToast = new Element();
const testToastTitle = new Element();
const testToastBody = new Element();
const autoScrollButton = new Element({'data-i18n': 'action.auto_scroll'});
const refreshLogButton = new Element({'data-i18n': 'action.refresh_log'});
const agentLogText = new Element({'data-i18n': 'status.loading'});
const agentLogMeta = new Element();
const systemEnvContent = new Element();
const saveSystemEnvButton = new Element();
const commentSystemEnvButton = new Element();
const systemEnvSection = new Element();
const exportLogsButton = new Element();
[
  ['test-stt', testButton], ['testToast', testToast], ['testToastTitle', testToastTitle],
  ['testToastBody', testToastBody], ['autoScrollAgentLogBtn', autoScrollButton],
  ['refreshAgentLogBtn', refreshLogButton], ['agentLogText', agentLogText],
  ['agentLogMeta', agentLogMeta], ['system_env_content', systemEnvContent],
  ['save-system_env', saveSystemEnvButton], ['comment-system_env', commentSystemEnvButton],
  ['section-system_env', systemEnvSection],
  ['exportLogsBtn', exportLogsButton],
].forEach(([id, element]) => elementsById.set(id, element));
elements.push(testButton, autoScrollButton, refreshLogButton, agentLogText);

const sttModule = await loadModule(path.join(webRoot, 'assets/js/config/stt-test.js'));
const logsModule = await loadModule(path.join(webRoot, 'assets/js/config/logs.js'));
const systemEnvModule = await loadModule(path.join(webRoot, 'assets/js/config/system-env.js'));
const configFormModule = await loadModule(path.join(webRoot, 'assets/js/config/config-form.js'));
await sttModule.evaluate();
await logsModule.evaluate();
await systemEnvModule.evaluate();
await configFormModule.evaluate();
registerRuntime({getSectionFields: () => ({agent: []}), readSection: () => ({})});

applyLocale('zh-CN', false);
sttModule.namespace.setSTTTestButtonState(true, false);
assert.equal(testButton.textContent, '结束录音');
applyLocale('en-US', false);
assert.equal(appState.sttTest.recording, true);
assert.equal(testButton.textContent, 'End Recording');

sttModule.namespace.setSTTTestButtonState(false, false);
requestResult = {sample_rate: 16000};
applyLocale('zh-CN', false);
await sttModule.namespace.startSTTTest();
assert.equal(testToastTitle.textContent, '[stt] 录音中');
assert.equal(testToastBody.textContent, 'sample_rate: 16000\n请说话，结束后再次点击测试按钮。');
applyLocale('en-US', false);
assert.equal(appState.sttTest.recording, true);
assert.equal(testToastTitle.textContent, '[stt] Recording');
assert.equal(testToastBody.textContent, 'sample_rate: 16000\nPlease speak, click Test button again after ending.');

requestResult = {ok: true, transcript: 'hello', results: []};
applyLocale('zh-CN', false);
await sttModule.namespace.stopSTTTest();
assert.equal(testToastTitle.textContent, '[stt] 识别完成');
assert.equal(testToastBody.textContent, '识别结果：\nhello\n\n');
applyLocale('en-US', false);
assert.equal(appState.sttTest.recording, false);
assert.equal(testToastTitle.textContent, '[stt] Recognition completed');
assert.equal(testToastBody.textContent, 'Recognition result:\nhello\n\n');

sttModule.namespace.setSTTTestButtonState(true, false);
requestResult = {ok: true, results: []};
applyLocale('zh-CN', false);
await sttModule.namespace.stopSTTTest();
assert.equal(testToastBody.textContent, '未返回识别结果');
applyLocale('en-US', false);
assert.equal(testToastBody.textContent, 'No recognition result returned');

sttModule.namespace.setSTTTestButtonState(true, false);
requestResult = {ok: false, results: []};
applyLocale('zh-CN', false);
await sttModule.namespace.stopSTTTest();
assert.equal(testToastTitle.textContent, '[stt] 测试失败');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, '[stt] test failed');

requestError = new Error('raw start failure');
applyLocale('zh-CN', false);
await sttModule.namespace.startSTTTest();
assert.equal(testToastTitle.textContent, '启动 [stt] 测试失败');
assert.equal(testToastBody.textContent, 'raw start failure');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, 'Failed to start [stt] test');
assert.equal(testToastBody.textContent, 'raw start failure');

sttModule.namespace.setSTTTestButtonState(true, false);
requestError = new Error('raw stop failure');
applyLocale('zh-CN', false);
await sttModule.namespace.stopSTTTest();
assert.equal(testToastTitle.textContent, '结束 [stt] 测试失败');
assert.equal(testToastBody.textContent, 'raw stop failure');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, 'Failed to end [stt] test');
assert.equal(testToastBody.textContent, 'raw stop failure');
requestError = null;

const sectionOwnerRequest = deferred();
requestImpl = () => sectionOwnerRequest.promise;
applyLocale('zh-CN', false);
const sectionOwnerTest = configFormModule.namespace.testSection('agent');
assert.equal(testToastTitle.textContent, '正在测试 [agent]');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, 'Testing [agent]');
sectionOwnerRequest.resolve({ok: true, results: []});
await sectionOwnerTest;
assert.equal(testToastTitle.textContent, '[agent] test passed');
applyLocale('zh-CN', false);
assert.equal(testToastTitle.textContent, '[agent] 测试通过');
requestImpl = null;

const closedSectionRequest = deferred();
requestImpl = () => closedSectionRequest.promise;
const closedSectionTest = configFormModule.namespace.testSection('agent');
assert.equal(testToast.classList.contains('show'), true);
configFormModule.namespace.closeTestToast();
assert.equal(testToast.classList.contains('show'), false);
assert.equal(appState.testToast.owner, null);
closedSectionRequest.resolve({ok: true, results: []});
await closedSectionTest;
assert.equal(testToast.classList.contains('show'), false);
assert.equal(appState.testToast.owner, null);

requestImpl = async () => ({ok: true, results: []});
await configFormModule.namespace.testSection('agent');
assert.equal(testToast.classList.contains('show'), true);
assert.equal(appState.testToast.owner, 'section');

sttModule.namespace.setSTTTestButtonState(false, false);
const closedSTTRequest = deferred();
requestImpl = () => closedSTTRequest.promise;
const closedSTTTest = sttModule.namespace.startSTTTest();
assert.equal(appState.testToast.owner, 'stt');
configFormModule.namespace.closeTestToast();
closedSTTRequest.resolve({sample_rate: 16000});
await closedSTTTest;
assert.equal(testToast.classList.contains('show'), false);
assert.equal(appState.testToast.owner, null);
assert.equal(appState.sttTest.recording, true);

const closedSTTStopRequest = deferred();
requestImpl = () => closedSTTStopRequest.promise;
const closedSTTStopTest = sttModule.namespace.stopSTTTest();
configFormModule.namespace.closeTestToast();
closedSTTStopRequest.resolve({ok: true, results: []});
await closedSTTStopTest;
assert.equal(testToast.classList.contains('show'), false);
assert.equal(appState.testToast.owner, null);
assert.equal(appState.sttTest.recording, false);

requestImpl = async () => ({sample_rate: 16000});
await sttModule.namespace.startSTTTest();
assert.equal(testToast.classList.contains('show'), true);
assert.equal(appState.testToast.owner, 'stt');

sttModule.namespace.setSTTTestButtonState(false, false);
const supersededSectionRequest = deferred();
const owningSTTRequest = deferred();
requestImpl = (url) => url === '/api/config/test' ? supersededSectionRequest.promise : owningSTTRequest.promise;
const supersededSectionTest = configFormModule.namespace.testSection('agent');
const owningSTTTest = sttModule.namespace.startSTTTest();
assert.equal(appState.testToast.owner, 'stt');
supersededSectionRequest.resolve({ok: true, results: []});
await supersededSectionTest;
assert.equal(appState.testToast.owner, 'stt');
assert.equal(testToastTitle.textContent, '[stt] 正在启用麦克风');
owningSTTRequest.resolve({sample_rate: 16000});
await owningSTTTest;
assert.equal(testToastTitle.textContent, '[stt] 录音中');

sttModule.namespace.setSTTTestButtonState(false, false);
const supersededSTTRequest = deferred();
const owningSectionRequest = deferred();
requestImpl = (url) => url === '/api/config/test' ? owningSectionRequest.promise : supersededSTTRequest.promise;
const supersededSTTTest = sttModule.namespace.startSTTTest();
const owningSectionTest = configFormModule.namespace.testSection('agent');
assert.equal(appState.testToast.owner, 'section');
supersededSTTRequest.resolve({sample_rate: 16000});
await supersededSTTTest;
assert.equal(appState.testToast.owner, 'section');
assert.equal(testToastTitle.textContent, '正在测试 [agent]');
owningSectionRequest.resolve({ok: false, results: []});
await owningSectionTest;
assert.equal(testToastTitle.textContent, '[agent] 测试未通过');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, '[agent] test not passed');

requestImpl = async () => { throw new Error('raw section test failure'); };
applyLocale('zh-CN', false);
await configFormModule.namespace.testSection('agent');
assert.equal(testToastTitle.textContent, '测试 [agent] 失败');
assert.equal(testToastBody.textContent, 'raw section test failure');
applyLocale('en-US', false);
assert.equal(testToastTitle.textContent, 'Test [agent] failed');
assert.equal(testToastBody.textContent, 'raw section test failure');
requestImpl = null;

applyLocale('zh-CN', false);
requestResult = {agent_log: {exists: false, log: '', size_bytes: 0}};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(agentLogText.children.at(-1).textContent, '日志文件不可用');
applyLocale('en-US', false);
assert.equal(agentLogText.children.at(-1).textContent, 'Log file is unavailable');

applyLocale('zh-CN', false);
requestResult = {agent_log: {exists: true, log: 'raw Agent log', size_bytes: 13, truncated: true}};
await logsModule.namespace.refreshAgentLog(false);
assert.match(agentLogMeta.textContent, /显示最新片段/);
logsModule.namespace.setAgentLogAutoScroll(false);
applyLocale('en-US', false);
assert.equal(appState.agentLogAutoScroll, false);
assert.equal(autoScrollButton.textContent, 'Jump to Bottom');
assert.match(agentLogMeta.textContent, /Showing latest segment/);

const renderedLogChildren = agentLogText.children.length;
applyLocale('zh-CN', false);
assert.equal(agentLogText.children.length, renderedLogChildren);
assert.equal(agentLogText.children.at(-1).textContent, 'raw Agent log');

requestResult = {agent_log: {exists: true, log: '', size_bytes: 0}};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(agentLogText.children.at(-1).textContent, '日志为空');
requestError = new Error('raw backend log failure');
appState.agentLogPendingSnapshot = {exists: true, log: 'stale pending log', size_bytes: 17};
await logsModule.namespace.refreshAgentLog(true);
assert.deepEqual(
  agentLogText.children.map((child) => child.textContent),
  ['加载 Agent 日志失败：', 'raw backend log failure'],
);
assert.deepEqual(latestBanner, {message: '刷新 Agent 日志失败。', failed: true});
assert.equal(latestDetails, 'raw backend log failure');
assert.equal(appState.agentLogPendingSnapshot, null);
logsModule.namespace.applyPendingAgentLogSnapshotIfIdle();
assert.deepEqual(
  agentLogText.children.map((child) => child.textContent),
  ['加载 Agent 日志失败：', 'raw backend log failure'],
);
applyLocale('en-US', false);
assert.deepEqual(
  agentLogText.children.map((child) => child.textContent),
  ['Failed to load Agent log:', 'raw backend log failure'],
);
requestError = null;
requestResult = {agent_log: {exists: true, log: '', size_bytes: 0}};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(agentLogText.children.at(-1).textContent, 'Log is empty');

requestError = new Error('second raw backend log failure');
await logsModule.namespace.refreshAgentLog(true);
selectionNode = agentLogText;
requestError = null;
requestResult = {agent_log: {exists: true, log: 'fresh raw Agent log', size_bytes: 19}};
await logsModule.namespace.refreshAgentLog(false);
selectionNode = null;
assert.equal(appState.agentLogFailureView, null);
assert.equal(agentLogText.children.at(-1).textContent, 'fresh raw Agent log');
applyLocale('zh-CN', false);
assert.equal(agentLogText.children.at(-1).textContent, 'fresh raw Agent log');

requestError = new Error('raw background refresh failure');
await logsModule.namespace.refreshAgentLog(false);
assert.match(agentLogMeta.textContent, /raw background refresh failure/);
applyLocale('en-US', false);
assert.match(agentLogMeta.textContent, /raw background refresh failure/);
requestError = null;
requestResult = {agent_log: {exists: true, log: 'recovered raw Agent log', size_bytes: 23}};
await logsModule.namespace.refreshAgentLog(false);
assert.doesNotMatch(agentLogMeta.textContent, /raw background refresh failure/);
applyLocale('zh-CN', false);
assert.doesNotMatch(agentLogMeta.textContent, /raw background refresh failure/);

requestResult = {agent_log: {exists: true, log: 'selected old body', path: '/old.log', size_bytes: 17}};
await logsModule.namespace.refreshAgentLog(false);
selectionNode = {parentNode: agentLogText.children.at(-1)};
requestResult = {
  agent_log: {exists: true, log: 'latest pending body', path: '/latest.log', size_bytes: 19, truncated: true},
};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(agentLogText.children.at(-1).textContent, 'selected old body');
assert.match(agentLogMeta.textContent, /\/latest\.log/);
assert.match(agentLogMeta.textContent, /显示最新片段/);
applyLocale('en-US', false);
assert.equal(agentLogText.children.at(-1).textContent, 'selected old body');
assert.match(agentLogMeta.textContent, /\/latest\.log/);
assert.match(agentLogMeta.textContent, /Showing latest segment/);
selectionNode = null;
logsModule.namespace.applyPendingAgentLogSnapshotIfIdle();
assert.equal(appState.agentLogPendingSnapshot, null);
assert.equal(agentLogText.children.at(-1).textContent, 'latest pending body');

const olderSuccess = deferred();
const newerSuccess = deferred();
const successRequests = [olderSuccess, newerSuccess];
requestImpl = () => successRequests.shift().promise;
const olderSuccessRefresh = logsModule.namespace.refreshAgentLog(false);
const newerSuccessRefresh = logsModule.namespace.refreshAgentLog(false);
assert.equal(refreshLogButton.disabled, true);
newerSuccess.resolve({agent_log: {exists: true, log: 'newer success body', size_bytes: 18}});
await newerSuccessRefresh;
assert.equal(agentLogText.children.at(-1).textContent, 'newer success body');
assert.equal(refreshLogButton.disabled, false);
olderSuccess.resolve({agent_log: {exists: true, log: 'older success body', size_bytes: 18}});
await olderSuccessRefresh;
assert.equal(agentLogText.children.at(-1).textContent, 'newer success body');
assert.equal(refreshLogButton.disabled, false);

const olderFailure = deferred();
const newestSuccess = deferred();
const mixedRequests = [olderFailure, newestSuccess];
requestImpl = () => mixedRequests.shift().promise;
const olderFailureRefresh = logsModule.namespace.refreshAgentLog(false);
const newestSuccessRefresh = logsModule.namespace.refreshAgentLog(false);
newestSuccess.resolve({agent_log: {exists: true, log: 'newest success body', size_bytes: 19}});
await newestSuccessRefresh;
assert.equal(agentLogText.children.at(-1).textContent, 'newest success body');
assert.equal(refreshLogButton.disabled, false);
olderFailure.reject(new Error('obsolete background failure'));
await olderFailureRefresh;
assert.equal(agentLogText.children.at(-1).textContent, 'newest success body');
assert.doesNotMatch(agentLogMeta.textContent, /obsolete background failure/);
assert.equal(refreshLogButton.disabled, false);

const earlyOlderSuccess = deferred();
const lateNewerSuccess = deferred();
const buttonRequests = [earlyOlderSuccess, lateNewerSuccess];
requestImpl = () => buttonRequests.shift().promise;
const earlyOlderRefresh = logsModule.namespace.refreshAgentLog(false);
const lateNewerRefresh = logsModule.namespace.refreshAgentLog(false);
earlyOlderSuccess.resolve({agent_log: {exists: true, log: 'ignored early body', size_bytes: 18}});
await earlyOlderRefresh;
assert.equal(refreshLogButton.disabled, true);
lateNewerSuccess.resolve({agent_log: {exists: true, log: 'late newer body', size_bytes: 17}});
await lateNewerRefresh;
assert.equal(refreshLogButton.disabled, false);
assert.equal(agentLogText.children.at(-1).textContent, 'late newer body');

requestImpl = null;
requestResult = {agent_log: {exists: true, log: 'visible current body', path: '/current.log', size_bytes: 20}};
await logsModule.namespace.refreshAgentLog(false);
selectionNode = {parentNode: agentLogText.children.at(-1)};
requestResult = {agent_log: {exists: true, log: 'stale pending body', path: '/stale.log', size_bytes: 18}};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(appState.agentLogPendingSnapshot.log, 'stale pending body');
requestResult = {agent_log: {exists: true, log: 'visible current body', path: '/latest-current.log', size_bytes: 20}};
await logsModule.namespace.refreshAgentLog(false);
assert.equal(appState.agentLogPendingSnapshot, null);
assert.match(agentLogMeta.textContent, /\/latest-current\.log/);
selectionNode = null;
logsModule.namespace.applyPendingAgentLogSnapshotIfIdle();
assert.equal(agentLogText.children.at(-1).textContent, 'visible current body');

applyLocale('zh-CN', false);
systemEnvContent.value = 'ORIGINAL=1';
systemEnvModule.namespace.enterSystemEnvEdit();
assert.deepEqual(latestBanner, {message: '正在编辑 env，修改后请点击保存。', failed: false});
systemEnvContent.value = 'CHANGED=1';
systemEnvModule.namespace.cancelSystemEnvEdit();
assert.equal(systemEnvContent.value, 'ORIGINAL=1');
assert.deepEqual(latestBanner, {message: '已取消 env 修改。', failed: false});

systemEnvModule.namespace.enterSystemEnvEdit();
requestError = new Error('raw env save failure');
await systemEnvModule.namespace.saveSystemEnv();
assert.deepEqual(latestBanner, {message: '保存 env 失败。', failed: true});
assert.equal(latestDetails, 'raw env save failure');
requestError = null;
requestResult = {system_env: 'A=1'};
systemEnvContent.value = 'A=1';
await systemEnvModule.namespace.saveSystemEnv();
assert.deepEqual(latestBanner, {message: 'env 已保存。', failed: false});

fetchImpl = async () => ({ok: true, blob: async () => ({})});
await logsModule.namespace.exportLogs();
assert.deepEqual(latestBanner, {message: '日志已导出。', failed: false});
fetchImpl = async () => ({
  ok: false,
  status: 500,
  text: async () => JSON.stringify({error: 'raw export failure'}),
});
await logsModule.namespace.exportLogs();
assert.deepEqual(latestBanner, {message: '导出日志失败。', failed: true});
assert.equal(latestDetails, 'raw export failure');

const moduleDirectory = path.join(webRoot, 'assets/js/config');
const moduleNames = (await fs.readdir(moduleDirectory)).filter((name) => name.endsWith('.js'));
const moduleSource = (
  await Promise.all(moduleNames.map((name) => fs.readFile(path.join(moduleDirectory, name), 'utf8')))
).join('\n');
const runtimeDependencies = new Set(
  [...moduleSource.matchAll(/runtime(?:Function|Object)\('([^']+)'\)/g)].map((match) => match[1]),
);
const runtimeBindings = new Set();
for (const match of moduleSource.matchAll(/registerRuntime\s*\(\s*\{([\s\S]*?)\}\s*\);/g)) {
  for (const entry of match[1].split(',')) {
    const name = entry.trim().split(':')[0].trim();
    if (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name)) runtimeBindings.add(name);
  }
}
assert.deepEqual(
  [...runtimeDependencies].filter((name) => !runtimeBindings.has(name)).sort(),
  [],
  'every runtime dependency must be registered by a config web module',
);
