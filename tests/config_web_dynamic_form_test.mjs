import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const webRoot = path.join(repositoryRoot, 'src/config_web/web');

class ClassList {
  constructor(element) {
    this.element = element;
    this.values = new Set();
  }

  set(value) {
    this.values = new Set(String(value || '').split(/\s+/).filter(Boolean));
  }

  add(...values) {
    values.forEach((value) => this.values.add(value));
  }

  remove(...values) {
    values.forEach((value) => this.values.delete(value));
  }

  contains(value) {
    return this.values.has(value);
  }

  toString() {
    return [...this.values].join(' ');
  }
}

class Element {
  constructor(tagName) {
    this.tagName = String(tagName).toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.dataset = {};
    this.classList = new ClassList(this);
    this.textContent = '';
    this.type = '';
    this.value = '';
    this.checked = false;
    this.disabled = false;
    this.inert = false;
    this.open = false;
  }

  set id(value) {
    this.setAttribute('id', value);
  }

  get id() {
    return this.getAttribute('id') || '';
  }

  set className(value) {
    this.classList.set(value);
    this.attributes.set('class', this.classList.toString());
  }

  get className() {
    return this.classList.toString();
  }

  get options() {
    return this.tagName === 'SELECT' ? this.children : undefined;
  }

  set innerHTML(value) {
    this._innerHTML = String(value);
    this.children.forEach((child) => { child.parentNode = null; });
    this.children = [];
  }

  get innerHTML() {
    return this._innerHTML || '';
  }

  setAttribute(name, value) {
    const normalized = String(value);
    this.attributes.set(name, normalized);
    if (name === 'class') this.classList.set(normalized);
    if (name.startsWith('data-')) {
      const key = name.slice(5).replace(/-([a-z])/g, (_match, letter) => letter.toUpperCase());
      this.dataset[key] = normalized;
    }
  }

  getAttribute(name) {
    return this.attributes.has(name) ? this.attributes.get(name) : null;
  }

  hasAttribute(name) {
    return this.attributes.has(name);
  }

  removeAttribute(name) {
    this.attributes.delete(name);
  }

  removeChild(child) {
    this.children = this.children.filter((item) => item !== child);
    child.parentNode = null;
    return child;
  }

  appendChild(child) {
    if (child.parentNode) {
      child.parentNode.children = child.parentNode.children.filter((item) => item !== child);
    }
    child.parentNode = this;
    this.children.push(child);
    return child;
  }

  replaceChildren(...children) {
    this.children.forEach((child) => { child.parentNode = null; });
    this.children = [];
    children.forEach((child) => this.appendChild(child));
  }

  closest(selector) {
    let current = this;
    while (current) {
      if (selector === '.field' && current.classList.contains('field')) return current;
      current = current.parentNode;
    }
    return null;
  }

  querySelector(selector) {
    return findElement(this, selector, false);
  }

  querySelectorAll(selector) {
    return findElements(this, selector, false);
  }
}

function selectorMatches(element, selector) {
  const presenceMatch = selector.match(/^\[([^=\]]+)\]$/);
  if (presenceMatch) return element.hasAttribute(presenceMatch[1]);
  const attributeMatch = selector.match(/^\[([^=]+)=(?:"([^"]+)"|([^\]]+))\]$/);
  if (attributeMatch) return element.getAttribute(attributeMatch[1]) === (attributeMatch[2] ?? attributeMatch[3]);
  return false;
}

function findElement(root, selector, includeRoot = true) {
  if (includeRoot && selectorMatches(root, selector)) return root;
  for (const child of root.children) {
    const found = findElement(child, selector, true);
    if (found) return found;
  }
  return null;
}

function findElements(root, selector, includeRoot = true, matches = []) {
  if (includeRoot && selectorMatches(root, selector)) matches.push(root);
  root.children.forEach((child) => findElements(child, selector, true, matches));
  return matches;
}

class Document {
  constructor() {
    this.body = new Element('body');
    this.listeners = new Map();
  }

  createElement(tagName) {
    return new Element(tagName);
  }

  getElementById(id) {
    return findById(this.body, id);
  }

  querySelector(selector) {
    return findElement(this.body, selector, true);
  }

  querySelectorAll(selector) {
    return findElements(this.body, selector, true);
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  dispatchEvent(event) {
    (this.listeners.get(event.type) || []).forEach((listener) => listener(event));
  }
}

function findById(root, id) {
  if (root.id === id) return root;
  for (const child of root.children) {
    const found = findById(child, id);
    if (found) return found;
  }
  return null;
}

function appendTarget(document, section, parent = document.body) {
  const target = document.createElement('div');
  target.className = 'grid';
  target.setAttribute('data-config-section', section);
  parent.appendChild(target);
  return target;
}

function appendSpecialField(document, target, pathName, controlId, tagName = 'select') {
  const field = document.createElement('div');
  field.className = 'field wide';
  field.setAttribute('data-config-field', pathName);
  const control = document.createElement(tagName);
  control.id = controlId;
  field.appendChild(control);
  target.appendChild(field);
  return field;
}

const document = new Document();
const productLocaleSelect = document.createElement('select');
productLocaleSelect.id = 'productLocaleSelect';
productLocaleSelect.value = 'en-US';
document.body.appendChild(productLocaleSelect);
const agentTimezoneSelect = document.createElement('select');
agentTimezoneSelect.id = 'agent_timezone';
agentTimezoneSelect.value = 'UTC';
document.body.appendChild(agentTimezoneSelect);
const agentTarget = appendTarget(document, 'agent');
const modelTarget = appendTarget(document, 'model');
const quickCaptureTarget = appendTarget(document, 'quick_capture');
const hidTarget = appendTarget(document, 'hid');
const hidDebugTarget = appendTarget(document, 'hid-debug');
const voiceModelCard = document.createElement('div');
voiceModelCard.id = 'section-voice_model';
voiceModelCard.className = 'section-card';
voiceModelCard.setAttribute('data-hide-when-empty', '');
document.body.appendChild(voiceModelCard);
const voiceModelTarget = appendTarget(document, 'voice_model', voiceModelCard);
const voiceModelProviderField = appendSpecialField(document, voiceModelTarget, 'voice_model.provider', 'voice_model_provider');
document.getElementById('voice_model_provider').setAttribute('data-section', 'voice_model');
const modelProviderField = appendSpecialField(document, modelTarget, 'model.provider', 'model_provider');
const modelNameField = appendSpecialField(document, modelTarget, 'model.model', 'model_model', 'input');
document.getElementById('model_provider').setAttribute('data-section', 'model');
const modelSelectorDetails = document.createElement('details');
modelSelectorDetails.id = 'modelSelectorDetails';
modelSelectorDetails.open = true;
modelSelectorDetails.setAttribute('data-section-lock', '');
modelNameField.appendChild(modelSelectorDetails);
const modelEditButton = document.createElement('button');
modelEditButton.setAttribute('data-section-target', 'model');
document.body.appendChild(modelEditButton);
const modelSaveButton = document.createElement('button');
modelSaveButton.id = 'save-model';
document.body.appendChild(modelSaveButton);

const context = vm.createContext({document, console, setTimeout, clearTimeout});
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
    const modulePath = specifier.split('?', 1)[0];
    return loadModule(path.resolve(path.dirname(referencingPath), modulePath));
  });
  return module;
}

const stateModule = await loadModule(path.join(webRoot, 'assets/js/config/state.js'));
await stateModule.evaluate();
stateModule.namespace.runtime.t = (key, params = {}) => String(params.defaultValue ?? key).replace(/\{\{([A-Za-z0-9_]+)\}\}/g, (_match, name) => params[name] ?? '');
stateModule.namespace.runtime.applyAudioArchiveAvailability = () => {};
stateModule.namespace.runtime.rememberModelProvider = () => {};
stateModule.namespace.runtime.refreshCurrentModelReasoningSpec = () => {};
stateModule.namespace.runtime.syncModelSelectorSummary = () => {};
stateModule.namespace.runtime.updateAllProviderActionStates = () => {};
stateModule.namespace.runtime.VoiceModelProvidersManager = {records: {}};
const configMetaModule = await loadModule(path.join(webRoot, 'assets/js/config/config-meta.js'));
await configMetaModule.evaluate();
const {bindFieldVisibility, buildConfigMeta, hydrateSelectField} = configMetaModule.namespace;

buildConfigMeta({sections: [
  {name: 'agent', fields: [
    {key: 'locale', label: 'Language', widget: 'select', enum: [{value: 'en-US'}, {value: 'zh-CN'}]},
    {key: 'timezone', label: 'Time zone', widget: 'select', enum: [{value: 'UTC'}, {value: 'Asia/Shanghai'}, {value: 'America/Los_Angeles'}]},
    {key: 'input_mode', label: 'Input mode', widget: 'select', enum: [{value: 'stt'}, {value: 'realtime'}]},
    {key: 'new_field', label: 'New field', help: 'Rendered from metadata.', placeholder: 'example', layout: 'wide', widget: 'text'},
    {key: 'defaulted', label: 'Defaulted', widget: 'text', default: 'value'},
    {key: 'secret_value', label: 'Secret value', widget: 'text', secret: true},
    {key: 'notes', label: 'Notes', widget: 'textarea', layout: 'wide'},
  ]},
  {name: 'model', fields: [
    {key: 'provider', label: 'provider', widget: 'select', layout: 'wide'},
    {key: 'model', label: 'model', widget: 'text', layout: 'wide'},
    {key: 'temperature', label: 'temperature', widget: 'number', nullable: true, default: 0.2, placeholderWhen: [
      {value: 1, when: {all: [{field: 'model.model', op: 'in', values: ['gemini-3.8-flash']}]}},
      {value: null, when: {all: [{field: 'model.provider', op: 'providerType', value: 'gemini'}]}},
    ]},
    {key: 'api_mode', label: 'api_mode', widget: 'select', layout: 'wide', enum: [
      {value: '', label: 'Chat Completions (compatible)', excludeProviders: ['gemini']},
      {value: 'responses', label: 'Responses (local context)', providers: ['openai']},
      {value: 'interactions', label: 'Interactions (local context)', providers: ['gemini']},
      {value: 'interactions_stateful', label: 'Interactions (provider context)', providers: ['gemini']},
    ]},
  ]},
  {name: 'quick_capture', fields: [
    {key: 'enabled', label: 'Enabled', widget: 'boolean', default: true},
    {key: 'gpio_pin', label: 'GPIO Pin', widget: 'number', default: 0},
    {key: 'screen_memory_ttl', label: 'Screen Memory TTL', widget: 'text', default: '90d'},
  ]},
  {name: 'hid', fields: [
    {key: 'keyboard_layout', label: 'Keyboard layout', widget: 'select', enum: [{value: 'qwerty'}]},
    {key: 'input_backend', label: 'Input backend', widget: 'select', enum: [{value: 'hid'}, {value: 'adb'}]},
  ]},
  {name: 'voice_model', fields: [
    {key: 'provider', label: 'Realtime provider', widget: 'select', visibleWhen: {all: [{field: 'agent.input_mode', op: 'eq', value: 'realtime'}]}},
    {key: 'api_key', label: 'API key', widget: 'text', secret: true, visibleWhen: {all: [{field: 'agent.input_mode', op: 'eq', value: 'realtime'}]}},
    {key: 'model', label: 'Model', widget: 'text', visibleWhen: {all: [{field: 'agent.input_mode', op: 'eq', value: 'realtime'}]}},
  ]},
]});

assert.equal(document.getElementById('agent_locale'), null, 'agent.locale remains rendered by the page-level locale control');
assert.deepEqual(productLocaleSelect.options.map((option) => option.value), ['en-US', 'zh-CN']);
assert.deepEqual(agentTimezoneSelect.options.map((option) => option.value), ['UTC', 'Asia/Shanghai', 'America/Los_Angeles']);
bindFieldVisibility();
stateModule.namespace.runtime.t = (_key, params = {}) => 'Localized ' + String(params.defaultValue ?? '');
document.dispatchEvent({type: 'aiden:locale-changed'});
assert.deepEqual(productLocaleSelect.options.map((option) => option.textContent), ['Localized en-US', 'Localized zh-CN']);
stateModule.namespace.runtime.t = (key, params = {}) => String(params.defaultValue ?? key).replace(/\{\{([A-Za-z0-9_]+)\}\}/g, (_match, name) => params[name] ?? '');
assert.equal(document.getElementById('agent_input_mode').tagName, 'SELECT');
assert.equal(document.getElementById('agent_new_field').getAttribute('placeholder'), 'example');
assert.equal(document.getElementById('agent_new_field').closest('.field').classList.contains('wide'), true);
assert.equal(document.getElementById('agent_new_field').closest('.field').children[0].textContent, 'New field');
assert.equal(document.getElementById('agent_new_field').closest('.field').children[0].getAttribute('data-i18n'), 'config.fields.agent.new_field.label');
assert.equal(document.getElementById('agent_new_field').closest('.field').children[2].textContent, 'Rendered from metadata.');
assert.equal(document.getElementById('agent_new_field').getAttribute('data-i18n-placeholder'), 'config.fields.agent.new_field.placeholder');
assert.equal(document.getElementById('agent_defaulted').getAttribute('placeholder'), 'Default: value');
assert.equal(document.getElementById('agent_defaulted').dataset.configDefaultPlaceholder, 'value');
assert.equal(document.getElementById('agent_secret_value').type, 'password');
assert.equal(document.getElementById('agent_secret_value').getAttribute('autocomplete'), 'off');
assert.equal(document.getElementById('agent_notes').tagName, 'TEXTAREA');
assert.equal(document.getElementById('agent_notes').classList.contains('prompt-compact'), true);
assert.equal(document.getElementById('model_provider').closest('.field'), modelProviderField, 'model provider manager DOM is preserved');
assert.equal(document.getElementById('model_model').closest('.field'), modelNameField, 'model selector DOM is preserved');
assert.equal(document.getElementById('voice_model_provider').closest('.field'), voiceModelProviderField, 'realtime provider manager DOM is preserved');
assert.equal(document.getElementById('model_temperature').type, 'number');
assert.equal(document.getElementById('quick_capture_enabled').type, 'checkbox');
assert.equal(document.getElementById('quick_capture_enabled').closest('.field').classList.contains('boolean-field'), true, 'boolean fields align the checkbox with their label');
assert.equal(document.getElementById('quick_capture_enabled').parentNode.classList.contains('boolean-control'), true, 'boolean controls keep the checkbox and label text in one flex row');
assert.equal(document.getElementById('quick_capture_enabled').parentNode.children[1].tagName, 'SPAN');
assert.equal(document.getElementById('quick_capture_enabled').parentNode.children[1].textContent, 'Enabled');
assert.equal(document.getElementById('quick_capture_gpio_pin').type, 'number');
assert.equal(document.getElementById('quick_capture_screen_memory_ttl').dataset.configDefaultPlaceholder, '90d');
assert.equal(document.getElementById('voice_model_api_key').type, 'password');
assert.deepEqual(quickCaptureTarget.children.map((field) => field.getAttribute('data-config-field')), [
  'quick_capture.enabled',
  'quick_capture.gpio_pin',
  'quick_capture.screen_memory_ttl',
]);
assert.deepEqual(agentTarget.children.map((field) => field.getAttribute('data-config-field')), [
  'agent.input_mode',
  'agent.new_field',
  'agent.defaulted',
  'agent.secret_value',
  'agent.notes',
]);

document.getElementById('agent_input_mode').value = 'stt';
configMetaModule.namespace.applyFieldVisibility(true);
assert.equal(voiceModelCard.classList.contains('hidden'), true, 'voice model card is hidden outside realtime mode');
document.getElementById('agent_input_mode').value = 'realtime';
configMetaModule.namespace.applyFieldVisibility(true);
assert.equal(voiceModelCard.classList.contains('hidden'), false, 'voice model card is visible in realtime mode');

const configFormModule = await loadModule(path.join(webRoot, 'assets/js/config/config-form.js'));
await configFormModule.evaluate();
assert.equal(JSON.stringify(configFormModule.namespace.voiceModeSaveEntries('realtime')), JSON.stringify([
  {section: 'agent', keys: ['input_mode'], scope: 'voice-mode'},
  {section: 'voice_model'},
]));
assert.equal(JSON.stringify(configFormModule.namespace.voiceModeSaveEntries('stt')), JSON.stringify([
  {section: 'agent', keys: ['input_mode'], scope: 'voice-mode'},
  {section: 'stt'},
  {section: 'tts'},
]), 'switching to classic mode saves both required provider references with the mode');
assert.equal(JSON.stringify(configFormModule.namespace.voiceModeSaveEntries('text')), JSON.stringify([
  {section: 'agent', keys: ['input_mode'], scope: 'voice-mode'},
]));
configFormModule.namespace.applyConfigValidation([{
  field: 'agent.input_mode',
  message: 'invalid input_mode: broken',
}]);
const invalidInputModeField = document.getElementById('agent_input_mode').closest('.field');
assert.equal(invalidInputModeField.classList.contains('config-invalid'), true, 'invalid config fields are highlighted');
assert.equal(document.getElementById('agent_input_mode').getAttribute('aria-invalid'), 'true');
assert.equal(invalidInputModeField.querySelector('[data-config-validation-error]').textContent, 'invalid input_mode: broken');
document.dispatchEvent({type: 'change', target: document.getElementById('agent_input_mode')});
assert.equal(invalidInputModeField.classList.contains('config-invalid'), false, 'editing clears the stale field error');
assert.equal(document.getElementById('agent_input_mode').getAttribute('aria-invalid'), null);
assert.equal(configFormModule.namespace.initialReadyMessage(true, {config_valid: false}), 'config.invalid_recovery');
// Language and time zone are static header controls rather than config-meta
// fields, so their recovery highlight depends on index.html tagging the two
// wrappers with the paths the validation errors name.
const localeField = document.createElement('div');
localeField.className = 'field';
localeField.setAttribute('data-config-field', 'agent.locale');
localeField.appendChild(productLocaleSelect);
document.body.appendChild(localeField);
const timezoneField = document.createElement('div');
timezoneField.className = 'field';
timezoneField.setAttribute('data-config-field', 'agent.timezone');
timezoneField.appendChild(agentTimezoneSelect);
document.body.appendChild(timezoneField);
configFormModule.namespace.applyConfigValidation([
  {field: 'locale', message: 'invalid locale: fr-FR (expected zh-CN or en-US)'},
  {field: 'timezone', message: 'unsupported timezone: Mars/Olympus'},
]);
assert.equal(localeField.classList.contains('config-invalid'), true, 'a locale error highlights the language field');
assert.equal(productLocaleSelect.getAttribute('aria-invalid'), 'true');
assert.equal(timezoneField.classList.contains('config-invalid'), true, 'a timezone error highlights the time-zone field');
assert.equal(agentTimezoneSelect.getAttribute('aria-invalid'), 'true');
assert.equal(timezoneField.querySelector('[data-config-validation-error]').textContent, 'unsupported timezone: Mars/Olympus');
configFormModule.namespace.applyConfigValidation([]);
assert.equal(timezoneField.classList.contains('config-invalid'), false, 'a refreshed config clears the stale highlight');
assert.equal(agentTimezoneSelect.getAttribute('aria-invalid'), null);
const hidDebugField = document.getElementById('hid_input_backend').closest('.field');
hidDebugTarget.appendChild(hidDebugField);
configFormModule.namespace.setSectionLocked('hid', true);
assert.equal(document.getElementById('hid_input_backend').disabled, true, 'moved HID debug fields are locked with the section');
configFormModule.namespace.setSectionLocked('hid', false);
assert.equal(document.getElementById('hid_input_backend').disabled, false, 'moved HID debug fields unlock with the section');
stateModule.namespace.appState.config = {
  agent: {input_mode: 'stt'},
  voice_model: {
    provider: '',
    has_api_key: true,
    model: 'saved-realtime-model',
    endpoint: 'wss://advanced.example.test/realtime',
    turn_detection: 'smart_turn',
  },
};
configFormModule.namespace.fillConfigForm(stateModule.namespace.appState.config);
assert.equal(voiceModelCard.classList.contains('hidden'), true);
assert.equal(document.getElementById('voice_model_api_key').value, '');
assert.equal(document.getElementById('voice_model_api_key').placeholder, 'config.secret_saved_placeholder');
assert.equal(JSON.stringify(configFormModule.namespace.readSection('voice_model')), JSON.stringify({
  provider: '',
  has_api_key: true,
  model: 'saved-realtime-model',
  endpoint: 'wss://advanced.example.test/realtime',
  turn_detection: 'smart_turn',
}), 'hidden realtime fields preserve their saved values');
document.getElementById('agent_input_mode').value = 'realtime';
configMetaModule.namespace.applyFieldVisibility(true);
document.getElementById('voice_model_model').value = 'updated-realtime-model';
assert.equal(JSON.stringify(configFormModule.namespace.readSection('voice_model')), JSON.stringify({
  provider: '',
  has_api_key: true,
  model: 'updated-realtime-model',
  endpoint: 'wss://advanced.example.test/realtime',
  turn_detection: 'smart_turn',
}), 'editing common realtime fields preserves advanced settings');

let voiceModePatch = null;
stateModule.namespace.runtime.request = async (_url, options) => {
  voiceModePatch = JSON.parse(options.body);
  return {
    config: {
      agent: {input_mode: 'realtime'},
      voice_model: {
        provider: 'qwen-main',
        has_api_key: true,
        model: 'updated-realtime-model',
        endpoint: 'wss://advanced.example.test/realtime',
        turn_detection: 'smart_turn',
      },
    },
  };
};
stateModule.namespace.runtime.setBanner = () => {};
stateModule.namespace.runtime.setDetails = () => {};
stateModule.namespace.runtime.refreshAgentStatus = () => {};
document.getElementById('voice_model_provider').value = 'qwen-main';
assert.equal(await configFormModule.namespace.saveVoiceMode(), true);
assert.equal(JSON.stringify(voiceModePatch), JSON.stringify({
  config: {
    agent: {input_mode: 'realtime'},
    voice_model: {provider: 'qwen-main', model: 'updated-realtime-model'},
  },
}), 'switching to realtime saves the mode and selected realtime provider atomically');

stateModule.namespace.appState.config = {
  agent: {input_mode: 'stt'},
  voice_model: {
    provider: '',
    has_api_key: true,
    model: 'saved-realtime-model',
  },
};
configFormModule.namespace.fillConfigForm(stateModule.namespace.appState.config);
document.getElementById('agent_input_mode').value = 'realtime';
configMetaModule.namespace.applyFieldVisibility(true);
document.getElementById('voice_model_provider').value = 'qwen-main';
stateModule.namespace.runtime.request = async () => { throw new Error('validation failed'); };
assert.equal(await configFormModule.namespace.saveVoiceMode(), false);
assert.equal(stateModule.namespace.appState.config.agent.input_mode, 'stt', 'a failed mode save restores authoritative mode state');
assert.equal(stateModule.namespace.appState.config.voice_model.provider, '', 'a failed mode save restores authoritative provider state');
assert.equal(document.getElementById('agent_input_mode').value, 'realtime', 'failed edits remain visible for correction');
assert.equal(document.getElementById('agent_input_mode').disabled, false, 'failed mode edits remain editable');
assert.equal(document.getElementById('voice_model_provider').value, 'qwen-main', 'failed provider selection remains visible for correction');
assert.equal(document.getElementById('voice_model_provider').disabled, false, 'failed provider selection remains editable');

// Persisted failures must use the server's resolved config, without replacing
// another card's draft. Exercise every form save entry point.
for (const save of [
  () => configFormModule.namespace.saveSection('model'),
  () => configFormModule.namespace.saveSections(['model'], 'section-model', 'save-model'),
  () => configFormModule.namespace.saveFieldGroups([{section: 'model'}], 'section-model', 'save-model'),
  () => configFormModule.namespace.saveSectionFields('model-provider', 'model', ['provider'], 'section-model', 'save-model'),
]) {
  const savedConfig = {
    agent: {input_mode: 'realtime', new_field: 'persisted'},
    model: {provider: 'old', model: 'saved-model'},
    voice_model: {provider: 'qwen-main'},
  };
  stateModule.namespace.appState.config = savedConfig;
  configFormModule.namespace.fillConfigForm(savedConfig);
  configFormModule.namespace.enterEditSection('agent');
  document.getElementById('agent_new_field').value = 'unsaved draft';
  configFormModule.namespace.enterEditSection('model');
  document.getElementById('model_provider').value = 'new';
  const authoritative = {...savedConfig, model: {provider: 'new', model: 'normalized-model', has_api_key: true}};
  stateModule.namespace.runtime.request = async (_url, options) => {
    const patch = JSON.parse(options.body).config;
    assert.deepEqual(Object.keys(patch), ['model'], 'saving the model does not include the other draft');
    assert.equal(stateModule.namespace.appState.config.model.provider, 'old', 'unconfirmed edits stay out of the persisted snapshot');
    throw Object.assign(new Error('prepare VAD failed'), {persisted: true, applied: false, config: authoritative});
  };
  await save();
  assert.equal(stateModule.namespace.appState.config.model.model, 'normalized-model', 'use the persisted server response on application failure');
  assert.equal(document.getElementById('agent_new_field').value, 'unsaved draft', 'preserve another section draft');
  assert.equal(stateModule.namespace.appState.config.agent.new_field, 'persisted');
  assert.equal(document.getElementById('model_provider').value, 'new');
  assert.equal(modelSaveButton.disabled, true, 'a completed save stays locked');
  configFormModule.namespace.cancelEditSection('agent');
}

// Successful scoped saves preserve a different draft in the same section.
stateModule.namespace.appState.config = {agent: {input_mode: 'realtime', new_field: 'saved', notes: 'old notes'}};
configFormModule.namespace.fillConfigForm(stateModule.namespace.appState.config);
configFormModule.namespace.enterEditSectionFields('notes', 'agent', ['notes']);
configFormModule.namespace.enterEditSectionFields('new-field', 'agent', ['new_field']);
document.getElementById('agent_notes').value = 'draft notes';
document.getElementById('agent_new_field').value = 'new saved';
stateModule.namespace.runtime.request = async (_url, options) => {
  assert.equal(JSON.stringify(JSON.parse(options.body).config), JSON.stringify({agent: {new_field: 'new saved'}}));
  return {config: {agent: {input_mode: 'realtime', new_field: 'new saved', notes: 'old notes'}}, pending: true};
};
await configFormModule.namespace.saveSectionFields('new-field', 'agent', ['new_field']);
assert.equal(document.getElementById('agent_notes').value, 'draft notes');
assert.equal(document.getElementById('agent_notes').disabled, false);
assert.equal(stateModule.namespace.appState.config.agent.notes, 'old notes');
configFormModule.namespace.cancelEditSectionFields('notes');

// A validation rejection must not restore an obsolete whole-section snapshot
// over another successful save that completed while this request was pending.
configFormModule.namespace.enterEditSectionFields('notes', 'agent', ['notes']);
document.getElementById('agent_notes').value = 'invalid notes';
let rejectNotes;
stateModule.namespace.runtime.request = () => new Promise((_resolve, reject) => { rejectNotes = reject; });
const rejectedNotes = configFormModule.namespace.saveSectionFields('notes', 'agent', ['notes']);
stateModule.namespace.appState.config = {agent: {input_mode: 'realtime', new_field: 'saved meanwhile', notes: 'old notes'}};
rejectNotes(new Error('invalid notes'));
await rejectedNotes;
assert.equal(stateModule.namespace.appState.config.agent.new_field, 'saved meanwhile');
assert.equal(document.getElementById('agent_notes').value, 'invalid notes');
assert.equal(document.getElementById('agent_notes').disabled, false);
configFormModule.namespace.cancelEditSectionFields('notes');

configFormModule.namespace.setSectionLocked('model', true);
assert.equal(document.getElementById('model_provider').disabled, true, 'locking a section disables its fields');
assert.equal(modelSaveButton.disabled, true, 'locking a section disables its save button');
assert.equal(modelEditButton.disabled, false, 'locking a section keeps its edit button enabled');
assert.equal(modelSelectorDetails.inert, true, 'locking a section disables composite controls');
assert.equal(modelSelectorDetails.open, false, 'locking a section closes composite controls');
configFormModule.namespace.setSectionLocked('model', false);
assert.equal(document.getElementById('model_provider').disabled, false, 'editing a section enables its fields');
assert.equal(modelSaveButton.disabled, false, 'editing a section enables its save button');
assert.equal(modelSelectorDetails.inert, false, 'editing a section enables composite controls');

// A choice a provider cannot use must never become that provider's default:
// Gemini has no compatible transport, so an unset api_mode has to render as the
// native Interactions mode instead of "Chat Completions".
appendSpecialField(document, modelTarget, 'model.api_mode', 'model_api_mode');
stateModule.namespace.runtime.resolveModelProviderType = (ref) => ref;
const modelProviderSelect = document.getElementById('model_provider');
const apiModeSelect = () => document.getElementById('model_api_mode');
modelProviderSelect.value = 'gemini';
hydrateSelectField('model', 'api_mode', '', false);
assert.deepEqual(apiModeSelect().options.map((option) => option.value), ['interactions', 'interactions_stateful']);
assert.equal(apiModeSelect().value, 'interactions', 'Gemini defaults to its native Interactions mode');
modelProviderSelect.value = 'openai';
hydrateSelectField('model', 'api_mode', '', false);
assert.deepEqual(apiModeSelect().options.map((option) => option.value), ['', 'responses']);
assert.equal(apiModeSelect().value, '', 'providers with a compatible transport keep the empty default');
modelProviderSelect.value = 'gemini';
hydrateSelectField('model', 'api_mode', 'interactions_stateful', false);
assert.equal(apiModeSelect().value, 'interactions_stateful', 'an explicit Gemini mode is preserved');

// Temperature placeholders follow the resolved provider type, including a
// named provider record. Known Gemini defaults win over the provider-level rule;
// unknown defaults remain empty instead of inheriting the global 0.2 fallback.
stateModule.namespace.modelProvidersByName['google-main'] = {type: 'gemini'};
configMetaModule.namespace.ensureSelectOption(modelProviderSelect, 'google-main');
modelProviderSelect.value = 'google-main';
document.getElementById('model_model').value = 'gemini-2.5-flash';
configMetaModule.namespace.applyFieldVisibility(false, 'model.provider');
assert.equal(document.getElementById('model_temperature').dataset.configDefaultPlaceholder, '', 'named native Gemini providers defer unknown defaults to Google');
document.getElementById('model_model').value = 'gemini-3.8-flash';
configMetaModule.namespace.applyFieldVisibility(false, 'model.model');
assert.equal(document.getElementById('model_temperature').dataset.configDefaultPlaceholder, '1', 'known Gemini model defaults override provider-level omission');
modelProviderSelect.value = 'openai';
document.getElementById('model_model').value = 'custom-model';
configMetaModule.namespace.applyFieldVisibility(false, 'model.provider');
assert.equal(document.getElementById('model_temperature').dataset.configDefaultPlaceholder, '0.2', 'non-Gemini models retain the global fallback');

const indexHtml = await fs.readFile(path.join(webRoot, 'index.html'), 'utf8');
assert.match(indexHtml, /data-config-section="agent"/);
assert.match(indexHtml, /data-config-section="quick_capture"/);
assert.match(indexHtml, /id="section-voice_model"/);
assert.match(indexHtml, /data-config-section="voice_model"/);
assert.match(indexHtml, /data-config-field="model\.provider"/);
assert.match(indexHtml, /class="field" data-config-field="agent\.locale"/, 'the language control is addressable by validation field path');
assert.match(indexHtml, /class="field" data-config-field="agent\.timezone"/, 'the time-zone control is addressable by validation field path');
assert.doesNotMatch(indexHtml, /id="agent_input_mode"/, 'ordinary controls must not be hand-maintained in index.html');
assert.match(indexHtml, /data-action="enter-edit-section" data-section-target="model"/);
assert.match(indexHtml, /id="log-settings-group"[\s\S]*class="product-subgroup-head"[\s\S]*data-i18n="groups\.logs"[\s\S]*data-action="enter-edit-section" data-section-target="log"[\s\S]*id="section-log"/, 'the Logs title and edit actions share one header row');
assert.doesNotMatch(indexHtml, /data-action="(?:enter-edit-section|cancel-edit-section|test-section|save-section)" data-section=/);
assert.doesNotMatch(indexHtml, /<button(?=[^>]*data-action="(?:enter-edit-section|cancel-edit-section|test-section|save-section)")(?![^>]*data-section-target=)[^>]*>/);
assert.match(indexHtml, /id="modelSelectorDetails"[^>]*data-section-lock/);
const appSource = await fs.readFile(path.join(webRoot, 'assets/js/config/app.js'), 'utf8');
assert.match(appSource, /target\.dataset\.sectionTarget/);
assert.doesNotMatch(appSource, /target\.dataset\.section;/);
const versionedAssetSources = [indexHtml, appSource];
for (const source of versionedAssetSources) {
  assert.doesNotMatch(source, /\?v=configuration-groups-/, 'Config Web relies on server no-cache headers instead of hand-maintained asset versions');
}

process.stdout.write('config web dynamic form tests passed\n');
