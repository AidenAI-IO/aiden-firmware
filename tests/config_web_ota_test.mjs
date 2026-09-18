import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const moduleRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');

// ota.js reads the OTA log file and reports the supervisor's exit marker. The
// marker lost its inner spaces, so the parser must keep recognising the spaced
// form that earlier agent builds wrote into logs still present on a device.
const document = {
  getElementById() { return null; },
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
    const modulePath = specifier.split('?', 1)[0];
    return loadModule(path.resolve(path.dirname(referencingPath), modulePath));
  });
  return module;
}

const stateModule = await loadModule(path.join(moduleRoot, 'state.js'));
await stateModule.evaluate();
stateModule.namespace.runtime.updateActionDetailsVisibility = () => {};
stateModule.namespace.runtime.setBanner = () => {};
stateModule.namespace.runtime.setDetails = () => {};
stateModule.namespace.runtime.request = async () => ({});
stateModule.namespace.runtime.renderLogText = () => {};
stateModule.namespace.runtime.formatLogSize = () => '';
stateModule.namespace.runtime.t = (key) => key;

const otaModule = await loadModule(path.join(moduleRoot, 'ota.js'));
await otaModule.evaluate();
const {extractOtaExitCode} = otaModule.namespace;

assert.equal(
  extractOtaExitCode('[config_web][ota] update_exited exit_code=7'),
  7,
  'compact marker',
);
assert.equal(
  extractOtaExitCode('2026-09-01T00:00:00Z [INFO] [config_web] [ota] update_exited exit_code=7'),
  7,
  'spaced marker written before the compact prefix landed',
);
assert.equal(
  extractOtaExitCode('[config_web] [ota] update_exited exit_code=1 and trailing text'),
  1,
  'spaced marker with trailing log content',
);
assert.equal(
  extractOtaExitCode(
    '[config_web] [ota] update_exited exit_code=3\n[config_web][ota] update_exited exit_code=0',
  ),
  0,
  'the newest marker wins',
);
assert.equal(
  extractOtaExitCode(
    '[config_web][ota] update_exited exit_code=0\n[config_web] [ota] update_exited exit_code=9',
  ),
  9,
  'a newer spaced marker still wins over an older compact one',
);
assert.equal(extractOtaExitCode('update finished with no marker'), null, 'absent marker');
assert.equal(extractOtaExitCode(''), null, 'empty log');
assert.equal(extractOtaExitCode(null), null, 'missing log');

console.log('config web ota exit marker checks passed');
