import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const configRoot = path.join(repositoryRoot, 'src/config_web/web/assets/js/config');
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

for (const runtimeRoute of [
  '/api/models?provider=',
  '/api/config-test/stt/start',
  '/api/config-test/stt/stop',
  '/api/storage/status',
  '/api/storage/format',
  '/api/storage/eject',
]) {
  const usesAgent = runtimeRoute.startsWith('/api/models?')
    ? bundle.includes('agentRequest(`/api/models?provider=')
    : bundle.includes(`agentRequest('${runtimeRoute}'`);
  assert.ok(usesAgent,
    `runtime route must use the Agent base URL: ${runtimeRoute}`);
}

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
