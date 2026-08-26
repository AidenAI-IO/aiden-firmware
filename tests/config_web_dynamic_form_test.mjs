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

  addEventListener() {}
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
const agentTarget = appendTarget(document, 'agent');
const modelTarget = appendTarget(document, 'model');
const quickCaptureTarget = appendTarget(document, 'quick_capture');
const voiceModelCard = document.createElement('div');
voiceModelCard.id = 'section-voice_model';
voiceModelCard.className = 'section-card';
voiceModelCard.setAttribute('data-hide-when-empty', '');
document.body.appendChild(voiceModelCard);
const voiceModelTarget = appendTarget(document, 'voice_model', voiceModelCard);
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
    return loadModule(path.resolve(path.dirname(referencingPath), specifier));
  });
  return module;
}

const stateModule = await loadModule(path.join(webRoot, 'assets/js/config/state.js'));
await stateModule.evaluate();
stateModule.namespace.runtime.t = (key, params = {}) => String(params.defaultValue ?? key).replace(/\{\{([A-Za-z0-9_]+)\}\}/g, (_match, name) => params[name] ?? '');
stateModule.namespace.runtime.applyAudioArchiveAvailability = () => {};
stateModule.namespace.runtime.rememberModelProvider = () => {};
stateModule.namespace.runtime.syncModelSelectorSummary = () => {};
stateModule.namespace.runtime.updateAllProviderActionStates = () => {};
const configMetaModule = await loadModule(path.join(webRoot, 'assets/js/config/config-meta.js'));
await configMetaModule.evaluate();
const {buildConfigMeta} = configMetaModule.namespace;

buildConfigMeta({sections: [
  {name: 'agent', fields: [
    {key: 'locale', label: 'Language', widget: 'select', enum: [{value: 'en-US'}]},
    {key: 'input_mode', label: 'Input mode', widget: 'select', enum: [{value: 'text'}, {value: 'stt'}, {value: 'realtime'}]},
    {key: 'new_field', label: 'New field', help: 'Rendered from metadata.', placeholder: 'example', layout: 'wide', widget: 'text'},
    {key: 'defaulted', label: 'Defaulted', widget: 'text', default: 'value'},
    {key: 'secret_value', label: 'Secret value', widget: 'text', secret: true},
    {key: 'notes', label: 'Notes', widget: 'textarea', layout: 'wide'},
  ]},
  {name: 'model', fields: [
    {key: 'provider', label: 'provider', widget: 'select', layout: 'wide'},
    {key: 'model', label: 'model', widget: 'text', layout: 'wide'},
    {key: 'temperature', label: 'temperature', widget: 'number'},
  ]},
  {name: 'quick_capture', fields: [
    {key: 'enabled', label: 'Enabled', widget: 'boolean', default: true},
    {key: 'gpio_pin', label: 'GPIO Pin', widget: 'number', default: 0},
    {key: 'screen_memory_ttl', label: 'Screen Memory TTL', widget: 'text', default: '90d'},
  ]},
  {name: 'voice_model', fields: [
    {key: 'api_key', label: 'API key', widget: 'text', secret: true, visibleWhen: {all: [{field: 'agent.input_mode', op: 'eq', value: 'realtime'}]}},
    {key: 'model', label: 'Model', widget: 'text', visibleWhen: {all: [{field: 'agent.input_mode', op: 'eq', value: 'realtime'}]}},
  ]},
]});

assert.equal(document.getElementById('agent_locale'), null, 'agent.locale remains rendered by the page-level locale control');
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
assert.equal(document.getElementById('model_temperature').type, 'number');
assert.equal(document.getElementById('quick_capture_enabled').type, 'checkbox');
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
stateModule.namespace.appState.config = {
  agent: {input_mode: 'stt'},
  voice_model: {
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
  has_api_key: true,
  model: 'saved-realtime-model',
  endpoint: 'wss://advanced.example.test/realtime',
  turn_detection: 'smart_turn',
}), 'hidden realtime fields preserve their saved values');
document.getElementById('agent_input_mode').value = 'realtime';
configMetaModule.namespace.applyFieldVisibility(true);
document.getElementById('voice_model_model').value = 'updated-realtime-model';
assert.equal(JSON.stringify(configFormModule.namespace.readSection('voice_model')), JSON.stringify({
  has_api_key: true,
  model: 'updated-realtime-model',
  endpoint: 'wss://advanced.example.test/realtime',
  turn_detection: 'smart_turn',
}), 'editing common realtime fields preserves advanced settings');
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

const indexHtml = await fs.readFile(path.join(webRoot, 'index.html'), 'utf8');
assert.match(indexHtml, /data-config-section="agent"/);
assert.match(indexHtml, /data-config-section="quick_capture"/);
assert.match(indexHtml, /id="section-voice_model"/);
assert.match(indexHtml, /data-config-section="voice_model"/);
assert.match(indexHtml, /data-config-field="model\.provider"/);
assert.doesNotMatch(indexHtml, /id="agent_input_mode"/, 'ordinary controls must not be hand-maintained in index.html');
assert.match(indexHtml, /data-action="enter-edit-section" data-section-target="model"/);
assert.doesNotMatch(indexHtml, /data-action="(?:enter-edit-section|cancel-edit-section|test-section|save-section)" data-section=/);
assert.doesNotMatch(indexHtml, /<button(?=[^>]*data-action="(?:enter-edit-section|cancel-edit-section|test-section|save-section)")(?![^>]*data-section-target=)[^>]*>/);
assert.match(indexHtml, /id="modelSelectorDetails"[^>]*data-section-lock/);
const appSource = await fs.readFile(path.join(webRoot, 'assets/js/config/app.js'), 'utf8');
assert.match(appSource, /target\.dataset\.sectionTarget/);
assert.doesNotMatch(appSource, /target\.dataset\.section;/);

process.stdout.write('config web dynamic form tests passed\n');
