import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const configRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');

for (const retiredPath of [
  'src/config_web.cpp',
  'src/config_web_static_assets.cpp',
  'src/config_web_static_assets.h',
  'src/system_env_parser.cpp',
  'src/system_env_parser.h',
  'src/wifi_config.cpp',
  'src/wifi_config.h',
  'tests/agent_stub_main.cpp',
  'tests/config_web_e2e_test.cpp',
  'tests/config_web_source_test.cpp',
  'tests/config_web_test_assets.h',
  'tests/system_env_parser_test.cpp',
  'tests/wifi_config_test.cpp',
]) {
  await assert.rejects(
    fs.access(path.join(repositoryRoot, retiredPath)),
    (error) => error?.code === 'ENOENT',
    `retired C++ Config Web file still exists: ${retiredPath}`,
  );
}

const rootCMake = await fs.readFile(path.join(repositoryRoot, 'CMakeLists.txt'), 'utf8');
assert.equal(rootCMake.includes('add_executable(config_web'), false, 'legacy C++ config_web target returned');

const sources = await Promise.all([
  'agent-status.js',
  'config-form.js',
  'i18n.js',
  'logs.js',
  'ota.js',
  'providers.js',
  'storage.js',
  'stt-test.js',
  'system-env.js',
  'wifi.js',
].map((name) => fs.readFile(path.join(configRoot, name), 'utf8')));
sources.push(await fs.readFile(path.join(repositoryRoot, 'src/config_web/web/assets/js/llm-logs.js'), 'utf8'));
const bundle = sources.join('\n');

for (const route of [
  '/api/config',
  '/api/config/schema',
  '/api/config/locale',
  '/api/config/test',
  '/api/models?provider=',
  '/api/config-test/stt/start',
  '/api/config-test/stt/stop',
  '/api/storage/status',
  '/api/storage/format',
  '/api/storage/eject',
  '/api/device/snapshot',
  '/api/device/status',
  '/api/device/reboot',
  '/api/network/wifi/scan',
  '/api/network/wifi/connection',
  '/api/system/environment',
  '/api/ota/status',
  '/api/ota/updates',
  '/api/logs/agent',
  '/api/logs/llm',
  '/api/logs/support',
]) {
  assert.ok(bundle.includes(route), `missing canonical frontend route: ${route}`);
}

assert.equal(bundle.includes('agentRequest'), false, 'Config Web still contains Agent cross-port requests');
assert.equal(bundle.includes("port='8080'"), false, 'Config Web still hard-codes the Agent port');

for (const retiredRoute of [
  "'/api/config/meta'",
  "'/api/agent/logs'",
  "'/api/logs/export'",
  '/api/llm-logs/',
  "'/api/ota/update'",
  "'/api/ota/logs'",
  "'/api/reboot'",
  "'/api/system/env'",
  '/api/wifi/',
]) {
  assert.equal(bundle.includes(retiredRoute), false, `retired frontend route remains: ${retiredRoute}`);
}

process.stdout.write('config web route tests passed\n');
