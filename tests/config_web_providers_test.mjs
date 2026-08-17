import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const moduleRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');

class Element {
  constructor() {
    this.children = [];
    this.dataset = {};
    this.disabled = false;
    this.innerHTML = '';
    this.value = '';
    const classes = new Set();
    this.classList = {
      add(...names) { names.forEach((name) => classes.add(name)); },
      remove(...names) { names.forEach((name) => classes.delete(name)); },
      contains(name) { return classes.has(name); },
    };
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  addEventListener() {}
  dispatchEvent() {}
  focus() {}
  remove() {}
  closest() { return this.parentField || null; }
  querySelector(selector) { return selector === 'small' ? this.helpElement || null : null; }
}

const elements = new Map();
const document = {
  body: new Element(),
  addEventListener() {},
  createElement() { return new Element(); },
  getElementById(id) { return elements.get(id) || null; },
  querySelector() { return null; },
};

class Event {
  constructor(type, options = {}) {
    this.type = type;
    this.bubbles = !!options.bubbles;
  }
}

const context = vm.createContext({
  alert() {},
  console,
  document,
  Event,
  fetch: (...args) => fetchImpl(...args),
  setTimeout() {},
  window: {confirm: () => true},
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

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return {promise, resolve, reject};
}

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

let requestImpl = async () => ({config: {}});
let fetchImpl = async () => { throw new Error('fetch is not configured'); };
let latestDetails = null;
let modelSectionEditing = false;
const modelProviderMeta = [
  {
    key: 'type',
    widget: 'select',
    enum: ['anthropic', 'openai', 'openrouter', 'ollama'].map((value) => ({value, label: value})),
  },
  {key: 'api_key', widget: 'text', secret: true},
  {
    key: 'base_url',
    widget: 'text',
    visibleWhen: {
      all: [{field: 'model_providers.type', op: 'in', values: ['anthropic', 'openai', 'ollama']}],
    },
  },
];
const recordSectionFields = {
  model_providers: modelProviderMeta.map((field) => [field.key, 'text', !!field.secret]),
  tts_providers: [],
  stt_providers: [],
};
const selectFieldOptions = {
  model: {provider: []},
  model_providers: {
    type: modelProviderMeta[0].enum.map((option) => ({...option, providers: []})),
  },
};

function applyProviderFieldVisibility() {
  const type = elements.get('model_providers_type')?.value || '';
  const baseURL = elements.get('model_providers_base_url');
  const field = baseURL?.parentField;
  if (!field) return;
  const condition = modelProviderMeta[2].visibleWhen.all[0];
  field.classList[condition.values.includes(type) ? 'remove' : 'add']('hidden');
}
const stateModule = await loadModule(path.join(moduleRoot, 'state.js'));
await stateModule.evaluate();
const {appState, registerRuntime} = stateModule.namespace;
registerRuntime({
  applyFieldVisibility: applyProviderFieldVisibility,
  ensureSelectOption() {},
  getActiveLocale: () => 'en-US',
  getRecordSectionFields: () => recordSectionFields,
  getSelectFieldOptions: () => selectFieldOptions,
  hydrateSelectField() {},
  isSectionEditing: (section) => section === 'model' && modelSectionEditing,
  optionValue: (option) => option.value,
  request: (...args) => requestImpl(...args),
  setBanner() {},
  setDetails: (message) => { latestDetails = message; },
  t: (key) => key,
});

const providersModule = await loadModule(path.join(moduleRoot, 'providers.js'));
await providersModule.evaluate();
const {
  ModelProvidersManager,
  ModelSelector,
  providerAPIKeyHelp,
  providerAPIKeyPlaceholder,
  syncProviderCredentialHelp,
  rememberModelProvider,
  syncModelProvidersFromConfig,
  editSelectedProvider,
  TtsProvidersManager,
  SttProvidersManager,
} = providersModule.namespace;

assert.equal(
  providerAPIKeyPlaceholder('anthropic', false),
  'provider.anthropic_api_key_placeholder',
  'new Anthropic providers should suggest Anthropic environment variables',
);
assert.equal(
  providerAPIKeyHelp('anthropic'),
  'provider.anthropic_api_key_help',
  'Anthropic providers should explain bearer and x-api-key authentication',
);
assert.equal(providerAPIKeyPlaceholder('openai', false), 'provider.api_key_placeholder');
assert.equal(providerAPIKeyHelp('openai'), 'provider.api_key_help');
assert.equal(
  providerAPIKeyPlaceholder('anthropic', true),
  'provider.credential_saved_placeholder',
  'editing a configured Anthropic provider should keep the write-only credential placeholder',
);

const providerTypeInput = new Element();
providerTypeInput.value = 'anthropic';
elements.set('model_providers_type', providerTypeInput);
const providerAPIKeyInput = new Element();
elements.set('model_providers_api_key', providerAPIKeyInput);
const providerAPIKeyHelpElement = new Element();
const providerAPIKeyField = new Element();
providerAPIKeyField.helpElement = providerAPIKeyHelpElement;
providerAPIKeyInput.parentField = providerAPIKeyField;
const providerBaseURLInput = new Element();
providerBaseURLInput.value = 'https://gateway.example.com/v1';
const providerBaseURLField = new Element();
providerBaseURLInput.parentField = providerBaseURLField;
elements.set('model_providers_base_url', providerBaseURLInput);
const providerNameInput = new Element();
elements.set('model_providers_record_name', providerNameInput);

syncProviderCredentialHelp('model_providers', false);
assert.equal(providerAPIKeyInput.placeholder, 'provider.anthropic_api_key_placeholder');
assert.equal(providerAPIKeyHelpElement.textContent, 'provider.anthropic_api_key_help');
ModelProvidersManager.dialogCredentialConfigured = false;
ModelProvidersManager.spec.credentialHint(ModelProvidersManager);
assert.equal(
  providerAPIKeyInput.placeholder,
  'provider.anthropic_api_key_placeholder',
  'the model-provider credential hook preserves the Anthropic-specific hint',
);
applyProviderFieldVisibility();
assert.equal(providerBaseURLField.classList.contains('hidden'), false);
providerTypeInput.value = 'openrouter';
applyProviderFieldVisibility();
assert.equal(providerBaseURLField.classList.contains('hidden'), true);
providerTypeInput.value = 'openai';
syncProviderCredentialHelp('model_providers', false);
assert.equal(providerAPIKeyInput.placeholder, 'provider.api_key_placeholder');
assert.equal(providerAPIKeyHelpElement.textContent, 'provider.api_key_help');
applyProviderFieldVisibility();
assert.equal(providerBaseURLField.classList.contains('hidden'), false);
assert.equal(
  ModelProvidersManager.spec.nameBase({type: 'openai', base_url: providerBaseURLInput.value}),
  'example',
  'OpenAI custom endpoints still provide an automatic record name',
);

const modelInput = new Element();
modelInput.value = 'existing-model';
elements.set('model_model', modelInput);
ModelSelector.selectModel('blocked-model');
assert.equal(modelInput.value, 'existing-model', 'model selection is immutable outside edit mode');
modelSectionEditing = true;
ModelSelector.selectModel('editable-model');
assert.equal(modelInput.value, 'editable-model', 'model selection updates while editing');
modelSectionEditing = false;

const modelSelectorContainer = new Element();
elements.set('modelSelectorContainer', modelSelectorContainer);
const modelSelectorSummary = new Element();
elements.set('modelSelectorSummary', modelSelectorSummary);
const providerSelectForModels = new Element();
elements.set('model_provider', providerSelectForModels);

const modelsByProvider = {
  openai: [
    {id: 'gpt-5.5', recommended: true},
    {id: 'gpt-4o', recommended: false},
  ],
  openrouter: [
    {id: 'anthropic/claude-opus-4-8', recommended: true},
    {id: 'google/gemini-3.5-pro', recommended: false},
  ],
};
const fetchModels = async (url) => {
  const provider = new URL(String(url), 'http://config.test').searchParams.get('provider');
  return {
    ok: true,
    status: 200,
    json: async () => ({models: modelsByProvider[provider] || []}),
  };
};
fetchImpl = fetchModels;

appState.config = {
  model: {provider: 'openai-main', model: 'gpt-4o'},
  model_providers: {
    'openai-main': {type: 'openai'},
    router: {type: 'openrouter'},
  },
};
providerSelectForModels.value = 'openai-main';
modelInput.value = 'gpt-4o';
syncModelProvidersFromConfig();
modelInput.value = 'stale-before-model-refresh';
await flushPromises();
await flushPromises();
assert.equal(modelInput.value, 'gpt-4o', 'initial provider sync seeds configured model before model list load');

providerSelectForModels.value = 'router';
rememberModelProvider();
await ModelSelector.onProviderChange('router');
assert.equal(
  modelInput.value,
  'anthropic/claude-opus-4-8',
  'provider with no remembered choice uses its recommended default',
);

modelSectionEditing = true;
ModelSelector.selectModel('google/gemini-3.5-pro');
modelSectionEditing = false;
assert.equal(modelInput.value, 'google/gemini-3.5-pro', 'edited provider choice is stored');

providerSelectForModels.value = 'openai-main';
rememberModelProvider();
await ModelSelector.onProviderChange('openai-main');
assert.equal(modelInput.value, 'gpt-4o', 'switching back restores the previous provider model');

providerSelectForModels.value = 'router';
rememberModelProvider();
await ModelSelector.onProviderChange('router');
assert.equal(modelInput.value, 'google/gemini-3.5-pro', 'switching back restores the new provider model');

ModelProvidersManager.load({'rename-old': {type: 'openai'}});
providerSelectForModels.value = 'rename-old';
rememberModelProvider();
modelInput.value = 'gpt-4o';
rememberModelProvider();
appState.config = {
  model: {provider: 'rename-old', model: 'gpt-4o'},
  model_providers: {'rename-old': {type: 'openai'}},
};
providerTypeInput.value = 'openai';
providerBaseURLInput.value = '';
providerAPIKeyInput.value = '';
providerNameInput.value = 'rename-new';
requestImpl = async () => ({config: {
  model: {provider: 'rename-new', model: 'gpt-4o'},
  model_providers: {'rename-new': {type: 'openai'}},
}});
assert.equal(await ModelProvidersManager.saveDialog('rename-old'), true);
assert.equal(providerSelectForModels.value, 'rename-new');
modelInput.value = 'stale-renamed-refresh';
await ModelSelector.onProviderChange('rename-new');
assert.equal(modelInput.value, 'gpt-4o', 'provider rename refresh preserves remembered model');

ModelProvidersManager.load({'rename-fail-old': {type: 'openai'}});
providerSelectForModels.value = 'rename-fail-old';
rememberModelProvider();
modelInput.value = 'gpt-4o';
rememberModelProvider();
appState.config = {
  model: {provider: 'rename-fail-old', model: 'gpt-4o'},
  model_providers: {'rename-fail-old': {type: 'openai'}},
};
providerNameInput.value = 'rename-fail-new';
requestImpl = async () => { throw new Error('rename failed'); };
assert.equal(await ModelProvidersManager.saveDialog('rename-fail-old'), false);
assert.equal(providerSelectForModels.value, 'rename-fail-old');
assert.deepEqual(JSON.parse(JSON.stringify(ModelProvidersManager.records)), {
  'rename-fail-old': {type: 'openai'},
});
ModelProvidersManager.records['rename-fail-new'] = {type: 'openai'};
modelInput.value = 'stale-failed-rename-target';
await ModelSelector.onProviderChange('rename-fail-new');
assert.equal(modelInput.value, 'gpt-5.5', 'failed rename does not migrate model memory');
await ModelSelector.onProviderChange('rename-fail-old');
assert.equal(modelInput.value, 'gpt-4o', 'failed rename keeps model memory on the old provider');

fetchImpl = async () => ({
  ok: false,
  status: 500,
  json: async () => ({}),
});
providerSelectForModels.value = 'failing';
rememberModelProvider();
await ModelSelector.onProviderChange('failing');
assert.equal(modelInput.value, '', 'failed model-list loads do not keep the previous provider model');

assert.equal(ModelProvidersManager.sanitizeName('__proto__'), '');
assert.equal(ModelProvidersManager.sanitizeName('constructor'), '');
assert.equal(TtsProvidersManager.sanitizeName('prototype'), '');
assert.equal(ModelProvidersManager.sanitizeName('work-openai'), 'work-openai');

ModelProvidersManager.load({initial: {type: 'openai'}});
ModelProvidersManager.records = {next: {type: 'openai'}};
requestImpl = async () => { throw new Error('save failed'); };
assert.equal(await ModelProvidersManager.save(), false);
assert.deepEqual(JSON.parse(JSON.stringify(ModelProvidersManager.records)), {
  initial: {type: 'openai'},
});

const firstRequest = deferred();
const secondRequest = deferred();
let activeRequests = 0;
let maxActiveRequests = 0;
const requestBodies = [];
requestImpl = async (_url, options) => {
  requestBodies.push(JSON.parse(options.body));
  activeRequests++;
  maxActiveRequests = Math.max(maxActiveRequests, activeRequests);
  try {
    return await (requestBodies.length === 1 ? firstRequest.promise : secondRequest.promise);
  } finally {
    activeRequests--;
  }
};

ModelProvidersManager.load({model: {type: 'openai'}});
ModelProvidersManager.records = {model: {type: 'openai', base_url: 'https://example.com'}};
TtsProvidersManager.load({voice: {type: 'fish-audio'}});
TtsProvidersManager.records = {voice: {type: 'fish-audio', reference_id: 'ref-1'}};
latestDetails = 'stale error';
const modelSave = ModelProvidersManager.save();
const voiceSave = TtsProvidersManager.save();
await flushPromises();
assert.equal(requestBodies.length, 1);
firstRequest.resolve({config: {
  model_providers: {model: {type: 'openai', base_url: 'https://example.com'}},
  tts_providers: {voice: {type: 'fish-audio'}},
}});
await modelSave;
assert.equal(latestDetails, '');
latestDetails = 'stale voice error';
await flushPromises();
assert.equal(requestBodies.length, 2);
secondRequest.resolve({config: {
  model_providers: {model: {type: 'openai', base_url: 'https://example.com'}},
  tts_providers: {voice: {type: 'fish-audio', reference_id: 'ref-1'}},
}});
await voiceSave;
assert.equal(maxActiveRequests, 1);
assert.equal(latestDetails, '');

const providerSelect = new Element();
providerSelect.value = 'active';
elements.set('model_provider', providerSelect);
fetchImpl = fetchModels;
appState.config = {
  model: {provider: 'active', model: 'gpt-4o'},
  model_providers: {
    active: {type: 'openai'},
    backup: {type: 'openrouter'},
  },
};
ModelProvidersManager.load(appState.config.model_providers);
modelInput.value = 'gpt-4o';
rememberModelProvider();
providerSelect.value = 'backup';
rememberModelProvider();
await ModelSelector.onProviderChange('backup');
modelSectionEditing = true;
ModelSelector.selectModel('google/gemini-3.5-pro');
modelSectionEditing = false;
providerSelect.value = 'active';
rememberModelProvider();
await ModelSelector.onProviderChange('active');
let deleteBody = null;
requestImpl = async (_url, options) => {
  deleteBody = JSON.parse(options.body);
  return {config: Object.assign({}, appState.config, deleteBody.config)};
};
assert.equal(await ModelProvidersManager.deleteRecord('active'), true);
assert.equal(deleteBody.config.model.provider, 'backup');
assert.deepEqual(Object.keys(deleteBody.config.model_providers), ['backup']);
await ModelSelector.onProviderChange('backup');
assert.equal(
  modelInput.value,
  'google/gemini-3.5-pro',
  'deleting the active provider must not overwrite the backup provider model memory',
);

const sttSelect = new Element();
sttSelect.value = 'stt-active';
elements.set('stt_provider', sttSelect);
SttProvidersManager.load({'stt-active': {type: 'stub-stt'}});
let unexpectedSttEdits = 0;
const originalSttEditRecord = SttProvidersManager.editRecord;
SttProvidersManager.editRecord = () => { unexpectedSttEdits++; };
editSelectedProvider('unknown-provider-kind');
SttProvidersManager.editRecord = originalSttEditRecord;
assert.equal(unexpectedSttEdits, 0, 'unknown provider kinds must be a safe no-op');

console.log('config web provider tests passed');
