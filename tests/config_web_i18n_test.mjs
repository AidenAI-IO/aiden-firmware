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
    this.textContent = '';
    this.value = '';
    this.disabled = false;
  }

  getAttribute(name) {
    return this.attributes.has(name) ? this.attributes.get(name) : null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }
}

const title = new Element({'data-i18n': 'page.title'});
const password = new Element({'data-i18n-placeholder': 'wifi.password_optional'});
const localeSelect = new Element();
const elements = [title, password];
const stored = new Map([['aiden.config.locale', 'en-US']]);
const document = {
  title: '',
  documentElement: {lang: ''},
  body: {},
  getElementById(id) {
    return id === 'localeSelect' ? localeSelect : null;
  },
  querySelectorAll(selector) {
    const attribute = selector.slice(1, -1);
    return elements.filter((element) => element.attributes.has(attribute));
  },
};
const context = vm.createContext({
  console,
  document,
  localStorage: {
    getItem(key) { return stored.get(key) || null; },
    setItem(key, value) { stored.set(key, value); },
  },
});
const moduleCache = new Map();

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
assert.match(configForm, /t\('config\.save_failed',\{section:/);
assert.match(wifi, /t\('wifi\.connected_to',\s*\{ssid\s*:/);
assert.match(wifi, /t\('wifi\.no_networks'\)/);
assert.match(wifi, /aiden:locale-changed/);
assert.doesNotMatch(wifi, /runtimeFunction\('localizedText'\)/);
assert.match(providers, /t\('provider\.choose_model'\)/);
assert.doesNotMatch(providers, /runtimeFunction\('localizedText'\)/);
assert.match(agentStatus, /t\('status\.state\.'/);
assert.match(ota, /t\('ota\.update_started'\)/);

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
