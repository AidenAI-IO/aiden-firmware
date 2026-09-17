import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createHash, randomBytes} from 'node:crypto';

const source = name => readFile(new URL(`../assets/js/config/${name}`, import.meta.url), 'utf8');
const moduleURL = code => `data:text/javascript;base64,${Buffer.from(code).toString('base64')}`;
const {createMaintenanceRequestID} = await import(moduleURL(await source('backup-session.js')));
const {sha256Hex} = await import(moduleURL(await source('backup-checksum.js')));
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test('HTTP request IDs are UUID v4 without randomUUID', () => {
  const httpCrypto = {getRandomValues: bytes => bytes.set(randomBytes(bytes.length))};
  for (let index = 0; index < 100; index += 1) assert.match(createMaintenanceRequestID(httpCrypto), uuid);
  assert.match(createMaintenanceRequestID({}), uuid);
});

test('HTTP SHA-256 fallback matches native hashes at padding and chunk boundaries', async () => {
  for (const size of [0, 1, 3, 55, 56, 63, 64, 65, 127, 128, 4096, 4 * 1024 * 1024]) {
    const bytes = randomBytes(size);
    assert.equal(await sha256Hex(bytes, {}), createHash('sha256').update(bytes).digest('hex'));
  }
  assert.equal(await sha256Hex(new TextEncoder().encode('abc'), {}), 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
});

test('backup and restore wizards have no component, mode, SD strategy or password inputs', async () => {
  const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
  for (const id of ['dataBackupMode', 'dataBackupComponents', 'dataBackupPassphrase', 'dataBackupPassphraseConfirm', 'dataRestorePassphrase', 'dataRestoreComponents', 'dataRestoreSDStrategy', 'dataBackupStep-restore-confirm', 'dataRestoreConfirmInput', 'dataRestoreApplyBtn', 'dataBackupCancelBtn']) {
    assert.ok(!html.includes(`id="${id}"`), `unexpected control ${id}`);
  }
  assert.ok(html.includes('<div class="card-head"><h3 id="dataBackupModalTitle">Backup and restore</h3></div>'));
  assert.match(html, /id="dataRestoreIdentityRow" class="field boolean-field"[^>]*><label class="boolean-control">/);
});

test('restore plan uses every manifest component and the inserted SD card', async () => {
  const code = await source('backup.js');
  const app = await source('app.js');
  assert.match(code, /const RESTORE_SD_STRATEGY = 'allow_different';/);
  assert.match(code, /restorePlanComponents = components\.map\(item => item\.id\);/);
  assert.match(code, /components: restorePlanComponents,/);
  assert.match(code, /sd_strategy: RESTORE_SD_STRATEGY,/);
  assert.match(code, /window\.confirm\(t\('backup\.restore_confirm'\)\)/);
  assert.match(code, /activeJob\.kind !== 'restore' && !window\.confirm\(t\('backup\.cancel_confirm'\)\)/);
  assert.doesNotMatch(code, /confirmDataRestore|dataRestoreConfirmInput|showStep\('restore-confirm'\)/);
  assert.doesNotMatch(app, /confirmDataRestore|confirm-data-restore/);
});

test('expired session is recreated once; create uses all components, same device and no password', async () => {
  let sessions = 0;
  let capabilities = 0;
  let downloads = 0;
  globalThis.document = {body: {classList: {toggle() {}}}, querySelectorAll: () => []};
  globalThis.window = {};
  globalThis.backupHTTPHarness = {
    elements: {dataBackupStartBtn: {}, dataBackupSdHint: {}},
    request: async (url, options = {}) => {
      if (url === '/api/maintenance/sessions') {
        sessions += 1;
        return {csrf_token: `csrf-${sessions}`, expires_at: new Date(Date.now() + 3600000).toISOString()};
      }
      if (url === '/api/backup/capabilities') {
        if (capabilities++ === 0) throw Object.assign(new Error('expired'), {status: 401, error: 'maintenance_session_required'});
        return {components: [{id: 'diagnostics', available: true, advanced: true}], sd: {mounted: false}};
      }
      if (url === '/api/backup/jobs') {
        assert.match(options.headers.get('X-Aiden-Request-ID'), uuid);
        assert.equal(options.headers.get('X-Aiden-CSRF-Token'), 'csrf-2');
        assert.deepEqual(JSON.parse(options.body), {format_version: 1, mode: 'same_device', protection: {mode: 'none'}});
        return {job_id: 'test-backup', state: 'ready', archive_url: '/api/backup/jobs/test-backup/archive'};
      }
      if (url === '/api/backup/jobs/test-backup') return {state: 'completed'};
      throw new Error(`unexpected request ${url}`);
    },
    download: () => { downloads += 1; },
  };
  const state = moduleURL("export const appState = {}; export const byId = id => globalThis.backupHTTPHarness.elements[id]; export const runtimeFunction = () => key => key;");
  const api = moduleURL("export const request = (...args) => globalThis.backupHTTPHarness.request(...args); export const setDetails = () => {};");
  const modalNames = ['openBackupModal', 'closeBackupModal', 'showStep', 'setBackupStatus', 'setCardStatus', 'setBackupProgress', 'hideCardProgress', 'renderComponents', 'selectedComponents', 'renderManifestSummary', 'setRestoreFileInfo', 'setDoneMessage', 'formatBytes'];
  const modal = moduleURL(modalNames.map(name => `export const ${name} = () => {};`).join('\n') + '\nexport const isBackupModalOpen = () => true;');
  const host = moduleURL("export const hasNativeTransfer = () => false; export const requestNativeDownload = () => false; export const requestNativeFile = () => false; export const requestNativeUpload = () => {}; export const cancelNativeTransfer = () => {}; export const readArchiveHeader = () => {}; export const triggerBrowserDownload = () => globalThis.backupHTTPHarness.download(); export const uploadBrowserChunks = () => {};");
  let code = await source('backup.js');
  for (const [name, url] of [['state.js', state], ['api.js', api], ['backup-modal.js', modal], ['host-transfer.js', host], ['backup-session.js', moduleURL(await source('backup-session.js'))]]) {
    code = code.replace(`'./${name}'`, JSON.stringify(url));
  }
  const backup = await import(moduleURL(code));
  await backup.createDataBackup();
  await backup.startDataBackup();
  assert.equal(sessions, 2);
  assert.equal(downloads, 1);
});
