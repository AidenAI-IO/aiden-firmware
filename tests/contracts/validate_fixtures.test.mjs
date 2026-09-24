import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const loadJSON = async name => JSON.parse(await readFile(path.join(root, name), 'utf8'));

test('shared UDS fixtures use the documented envelope', async () => {
  const request = await loadJSON('uds-health-request.json');
  assert.deepEqual(request, {type: 'request', method: 'health'});

  const encoded = await readFile(path.join(root, 'uds-frame-response.bin'));
  const headerSize = encoded.readUInt32LE(0);
  const payloadSize = Number(encoded.readBigUInt64LE(4));
  const headerStart = 12;
  const payloadStart = headerStart + headerSize;
  assert.equal(payloadStart + payloadSize, encoded.length);
  assert.deepEqual(JSON.parse(encoded.subarray(headerStart, payloadStart)), {
    type: 'response',
    method: 'latest_frame',
    seq: '7',
  });
  assert.deepEqual([...encoded.subarray(payloadStart)], [0, 1, 2, 3]);
});

test('shared Config Web and OTA fixtures preserve wire shapes', async () => {
  const config = await loadJSON('config-wire.json');
  assert.deepEqual(config.model, {provider: 'openai', model: 'gpt-4'});
  assert.deepEqual(config.search, {provider: 'duckduckgo'});
  assert.deepEqual(config.device, {device_type: 'iOS'});
  assert.deepEqual(config.agent, {});

  const manifest = await loadJSON('ota-manifest.json');
  assert.equal(manifest.schema_version, 2);
  assert.deepEqual(manifest.parts.map(part => part.name), ['boot', 'rootfs']);
  assert.equal(manifest.parts[0].asset_a.name, 'boot_a.img');
  assert.equal(manifest.parts[0].asset_b.name, 'boot_b.img');
  assert.equal(manifest.parts[1].asset.name, 'rootfs.img');
  assert.equal(Buffer.from(manifest.signature.value, 'base64').length, 64);
});
