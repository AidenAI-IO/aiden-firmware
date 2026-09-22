import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const moduleRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');
let fetchImpl = async () => { throw new Error('fetch is not configured'); };
const elements = new Map();
const context = vm.createContext({
  console,
  document: {getElementById(id) { return elements.get(id) || null; }, addEventListener() {}},
  setTimeout() { return 1; },
  clearTimeout() {},
  fetch: (...args) => fetchImpl(...args),
  URL,
  window: {location: {href: 'http://192.168.42.1/'}},
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
    const modulePath = specifier.split('?', 1)[0];
    return loadModule(path.resolve(path.dirname(referencingPath), modulePath));
  });
  return module;
}

const apiModule = await loadModule(path.join(moduleRoot, 'api.js'));
await apiModule.evaluate();
const {request} = apiModule.namespace;

fetchImpl = async () => ({ok: true, status: 200, text: async () => 'null'});
assert.deepEqual(JSON.parse(JSON.stringify(await request('/api/test'))), {});

fetchImpl = async () => ({ok: false, status: 503, text: async () => 'null'});
await assert.rejects(
  request('/api/test'),
  (error) => error.message === 'HTTP 503' && error.status === 503,
);

fetchImpl = async () => ({
  ok: false,
  status: 503,
  text: async () => JSON.stringify({error: 'agent unavailable', status: 200, persisted: true}),
});
await assert.rejects(
  request('/api/test'),
  (error) => error.message === 'agent unavailable' && error.status === 503 && error.persisted === true,
);

// Configuration writes share one queue, including locale changes. A failed
// write must release the next save while status reads remain available.
let releaseSave;
const started = [];
fetchImpl = async (url, options) => {
  started.push(url);
  if (options?.body === 'first') await new Promise((resolve) => {releaseSave = resolve;});
  return {ok: options?.body !== 'first', status: options?.body === 'first' ? 503 : 200, text: async () => '{}'};
};
const first = request('/api/config', {method:'PATCH', body:'first'});
const firstFailure = assert.rejects(first);
const second = request('/api/config/locale', {method:'PUT', body:'second'});
await request('/api/config/application', {method:'GET'});
assert.ok(started.includes('/api/config'));
assert.ok(!started.includes('/api/config/locale'));
releaseSave();
await firstFailure;
await second;
assert.equal(started.at(-1), '/api/config/locale');

// Manual TOML edits and backup imports are configuration saves too.
const stateModule = await loadModule(path.join(moduleRoot, 'state.js'));
const savedStatuses = [];
stateModule.namespace.registerRuntime({configApplicationSaved: (value) => savedStatuses.push(value)});
let releaseConfig;
started.length = 0;
fetchImpl = async (url) => {
  started.push(url);
  if (url === '/api/config') await new Promise((resolve) => {releaseConfig = resolve;});
  return {ok: true, status: 200, text: async () => JSON.stringify({persisted: true, state: 'pending', pending: true})};
};
const queuedConfig = request('/api/config', {method: 'PATCH', body: '{}'});
const queuedImport = request('/api/config/backup', {method: 'PUT', body: '[agent]'});
await request('/api/config/application', {method: 'GET'});
assert.ok(!started.includes('/api/config/backup'), 'imports wait for earlier configuration writes');
releaseConfig();
await Promise.all([queuedConfig, queuedImport]);
assert.equal(savedStatuses.length, 2, 'imports update the application status immediately');
fetchImpl = async () => ({ok: false, status: 503, text: async () => JSON.stringify({persisted: true, applied: false, error: 'VAD unavailable'})});
await assert.rejects(request('/api/config/backup', {method: 'PUT', body: '[agent]'}));
assert.equal(savedStatuses.at(-1).state, 'failed');
assert.equal(savedStatuses.at(-1).error, 'VAD unavailable');

// An older in-flight status read must not hide a failed TOML application.
for (const id of ['configApplication', 'configApplicationText', 'configApplyRetry', 'configReboot']) {
  elements.set(id, {style: {}, textContent: '', className: ''});
}
const applicationModule = await loadModule(path.join(moduleRoot, 'config-application.js'));
await applicationModule.evaluate();
let releaseStatus;
fetchImpl = async (url) => {
  if (url === '/api/config/application') {
    await new Promise((resolve) => {releaseStatus = resolve;});
    return {ok: true, status: 200, text: async () => JSON.stringify({state: 'applied', applied: true})};
  }
  return {ok: false, status: 503, text: async () => JSON.stringify({persisted: true, applied: false, error: 'VAD unavailable'})};
};
const staleStatus = applicationModule.namespace.refreshConfigApplication();
await assert.rejects(request('/api/config/backup', {method: 'PUT', body: '[agent]'}));
releaseStatus();
await staleStatus;
assert.equal(elements.get('configApplication').style.display, 'block');
assert.ok(elements.get('configApplicationText').textContent.includes('VAD unavailable'));
assert.equal(elements.get('configApplyRetry').style.display, '');

process.stdout.write('config web api tests passed\n');
