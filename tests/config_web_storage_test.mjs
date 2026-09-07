import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const moduleRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');

class ClassList {
  constructor() { this.values = new Set(); }
  add(...values) { values.forEach((value) => this.values.add(value)); }
  remove(...values) { values.forEach((value) => this.values.delete(value)); }
  contains(value) { return this.values.has(value); }
}

class Element {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.classList = new ClassList();
    this.style = {};
    this.textContent = '';
    this.disabled = false;
    this.value = 'fat32';
  }
  set className(value) {
    this.classList = new ClassList();
    String(value || '').split(/\s+/).filter(Boolean).forEach((name) => this.classList.add(name));
  }
  get className() { return [...this.classList.values].join(' '); }
}

const elements = new Map([
  ['storageSummary', new Element('div')],
  ['storageWarning', new Element('div')],
  ['storageFormatFs', new Element('select')],
  ['storageFormatBtn', new Element('button')],
  ['storageEjectBtn', new Element('button')],
  ['storageJobStatus', new Element('div')],
  ['storageRefreshBtn', new Element('button')],
]);
const document = {
  getElementById(id) { return elements.get(id) || null; },
  addEventListener() {},
};
const context = vm.createContext({document, console});
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

const stateModule = await loadModule(path.join(moduleRoot, 'state.js'));
await stateModule.evaluate();
stateModule.namespace.runtime.request = async () => ({ });
stateModule.namespace.runtime.setBanner = () => {};
stateModule.namespace.runtime.setDetails = () => {};
const storageModule = await loadModule(path.join(moduleRoot, 'storage.js'));
await storageModule.evaluate();
const {appState} = stateModule.namespace;
const {normalizeStorageStatus, renderStorage} = storageModule.namespace;

function resetControls() {
  elements.get('storageSummary').textContent = '';
  elements.get('storageWarning').textContent = '';
  elements.get('storageWarning').style.display = '';
  elements.get('storageJobStatus').textContent = '';
  elements.get('storageJobStatus').style.display = '';
  elements.get('storageJobStatus').className = '';
  ['storageFormatFs', 'storageFormatBtn', 'storageEjectBtn'].forEach((id) => { elements.get(id).disabled = false; });
}

resetControls();
renderStorage({
  effective_mode: 1,
  card: {present: false, mounted: false},
  mount_point: '/mnt/sdcard',
  format_job: {status: 'idle'},
  migration: {status: 'idle'},
});
assert.match(elements.get('storageSummary').textContent, /No SD card detected/);
assert.equal(elements.get('storageFormatBtn').disabled, true);
assert.equal(elements.get('storageEjectBtn').disabled, true);

resetControls();
renderStorage({
  effective_mode: 1,
  card: {present: true, mounted: false, reason: 'filesystem check failed'},
  mount_point: '/mnt/sdcard',
  format_job: {status: 'idle'},
  migration: {status: 'idle'},
});
assert.match(elements.get('storageSummary').textContent, /present but not usable/);
assert.equal(elements.get('storageFormatBtn').disabled, false);
assert.equal(elements.get('storageEjectBtn').disabled, true);
assert.equal(elements.get('storageWarning').textContent, 'Card issue: filesystem check failed');

resetControls();
renderStorage({
  effective_mode: 2,
  card: {present: true, mounted: true, total_bytes: 10 * 1024 ** 3, free_bytes: 4 * 1024 ** 3},
  mount_point: '/mnt/sdcard',
  format_job: {status: 'idle'},
  migration: {status: 'idle'},
});
assert.match(elements.get('storageSummary').textContent, /SD card mounted at \/mnt\/sdcard \(4\.0 GB free of 10\.0 GB\)/);
assert.equal(elements.get('storageFormatBtn').disabled, false);
assert.equal(elements.get('storageEjectBtn').disabled, false);

resetControls();
renderStorage({
  effective_mode: 2,
  card: {present: true, mounted: true},
  mount_point: '/mnt/sdcard',
  format_job: {status: 'running', fs: 'ext4', auto: true},
  migration: {status: 'idle'},
});
assert.equal(elements.get('storageFormatBtn').disabled, true);
assert.equal(elements.get('storageEjectBtn').disabled, true);
assert.match(elements.get('storageJobStatus').textContent, /auto-formatting as ext4/);

const retiredShape = normalizeStorageStatus({available: true, sd_present: true, sd_mounted: true, total_bytes: 8, free_bytes: 3});
assert.equal(JSON.stringify(retiredShape), JSON.stringify({
  available: false, present: false, mounted: false, device: '', mountPoint: '',
  reason: '', effectiveMode: undefined,
  formatJob: {}, migration: {},
}));

process.stdout.write('config web storage tests passed\n');
