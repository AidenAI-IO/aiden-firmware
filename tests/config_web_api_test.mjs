import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const moduleRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');
let fetchImpl = async () => { throw new Error('fetch is not configured'); };
const context = vm.createContext({
  console,
  document: {getElementById() { return null; }},
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
    return loadModule(path.resolve(path.dirname(referencingPath), specifier));
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

process.stdout.write('config web api tests passed\n');
