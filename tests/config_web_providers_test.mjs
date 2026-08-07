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
    this.classList = {add() {}, remove() {}, contains() { return false; }};
  }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  addEventListener() {}
  dispatchEvent() {}
  focus() {}
  remove() {}
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
  fetch: async () => { throw new Error('fetch is not configured'); },
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
let latestDetails = null;
const stateModule = await loadModule(path.join(moduleRoot, 'state.js'));
await stateModule.evaluate();
const {appState, registerRuntime} = stateModule.namespace;
registerRuntime({
  applyFieldVisibility() {},
  ensureSelectOption() {},
  getActiveLocale: () => 'en-US',
  getRecordSectionFields: () => ({tts_providers: [], stt_providers: []}),
  getSelectFieldOptions: () => ({}),
  hydrateSelectField() {},
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
  TtsProvidersManager,
} = providersModule.namespace;

assert.equal(ModelProvidersManager.sanitizeName('__proto__'), '');
assert.equal(ModelProvidersManager.sanitizeName('constructor'), '');
assert.equal(TtsProvidersManager.sanitizeName('prototype'), '');
assert.equal(ModelProvidersManager.sanitizeName('work-openai'), 'work-openai');

const source = await fs.readFile(path.join(moduleRoot, 'providers.js'), 'utf8');
assert.match(source, /renamedFrom\?this\.modelRefPatch\(renamedFrom,name\):null/);

ModelProvidersManager.loadModelProviders({initial: {type: 'openai'}});
ModelProvidersManager.modelProviders = {next: {type: 'openai'}};
requestImpl = async () => { throw new Error('save failed'); };
await ModelProvidersManager.saveModelProviders();
assert.deepEqual(JSON.parse(JSON.stringify(ModelProvidersManager.modelProviders)), {
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

ModelProvidersManager.loadModelProviders({model: {type: 'openai'}});
ModelProvidersManager.modelProviders = {model: {type: 'openai', base_url: 'https://example.com'}};
TtsProvidersManager.load({voice: {type: 'fish-audio'}});
TtsProvidersManager.records = {voice: {type: 'fish-audio', reference_id: 'ref-1'}};
latestDetails = 'stale error';
const modelSave = ModelProvidersManager.saveModelProviders();
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
appState.config = {
  model: {provider: 'active', model: 'gpt-test'},
  model_providers: {
    active: {type: 'openai'},
    backup: {type: 'openrouter'},
  },
};
ModelProvidersManager.loadModelProviders(appState.config.model_providers);
let deleteBody = null;
requestImpl = async (_url, options) => {
  deleteBody = JSON.parse(options.body);
  return {config: Object.assign({}, appState.config, deleteBody.config)};
};
await ModelProvidersManager.deleteProvider('active');
assert.equal(deleteBody.config.model.provider, 'backup');
assert.deepEqual(Object.keys(deleteBody.config.model_providers), ['backup']);

console.log('config web provider tests passed');
