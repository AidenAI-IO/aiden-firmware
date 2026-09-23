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
    this.value = '';
    this.textContent = '';
    this.disabled = false;
    this._classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((name) => this._classes.add(name)),
      remove: (...names) => names.forEach((name) => this._classes.delete(name)),
      toggle: (name, force) => {
        const enabled = force === undefined ? !this._classes.has(name) : !!force;
        if (enabled) this._classes.add(name);
        else this._classes.delete(name);
      },
    };
  }

  get className() { return [...this._classes].join(' '); }

  set className(value) { this._classes = new Set(String(value || '').split(/\s+/).filter(Boolean)); }

  appendChild(child) {
    this.children.push(child);
    return child;
  }

  setAttribute() {}

  removeAttribute() {}

  setCustomValidity() {}

  reportValidity() {}

  focus() {}
}

const elements = new Map();
for (const id of [
  'wifiList', 'wifiModal', 'wifiModalText', 'wifiPasswordInput', 'wifiProxyMode',
  'wifiProxyUrl', 'wifiNoProxy', 'wifiProxyUrlField', 'wifiNoProxyField', 'connectWifiBtn',
]) elements.set(id, new Element());
elements.get('wifiProxyMode').value = 'system';

const document = {
  getElementById(id) { return elements.get(id) || null; },
  createElement() { return new Element(); },
  addEventListener() {},
};
const context = vm.createContext({console, document, setTimeout, window: {confirm: () => true}});
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
const {appState, registerRuntime} = stateModule.namespace;
const requests = [];
let requestGate = null;
let requestResult = {
  ok: true,
  wifi: {country: 'CN', networks: [{ssid: 'Office', has_psk: true, priority: 1}]},
  wifi_status: {connected: true, ssid: 'Office', ip_address: '192.0.2.10'},
};
registerRuntime({
  request: async (_url, options) => {
    requests.push({url: _url, options});
    if (requestGate) await requestGate;
    return requestResult;
  },
  setBanner() {},
  setDetails() {},
  t: (key) => key,
});

const wifiModule = await loadModule(path.join(moduleRoot, 'wifi.js'));
await wifiModule.evaluate();
appState.wifi = {networks: [{ssid: 'Office', has_psk: true, priority: 1, proxy_mode: 'system'}]};
appState.wifiStatus = {};
wifiModule.namespace.renderWifiList();
const savedRow = elements.get('wifiList').children[0];
const savedActions = savedRow.children[1].children;
assert.equal(savedActions[0].dataset.action, 'open-wifi-modal');
assert.equal(savedActions[0].dataset.ssid, 'Office');
assert.equal(savedActions[0].textContent, 'action.edit');
assert.equal(savedActions[1].dataset.action, 'forget-wifi');

await wifiModule.namespace.connectSavedWifi('Office');
assert.equal(requests.length, 1);
assert.equal(requests[0].url, '/api/network/wifi/connection');
const body = JSON.parse(requests[0].options.body);
assert.deepEqual(body, {ssid: 'Office', proxy_mode: 'system'});
assert.equal(elements.get('wifiModal').className, '', 'saved Wi-Fi should not open the password modal');

requests.length = 0;
let releaseRequest;
requestGate = new Promise((resolve) => { releaseRequest = resolve; });
const firstConnection = wifiModule.namespace.connectSavedWifi('Office');
assert.equal(elements.get('connectWifiBtn').disabled, true);
const duplicateConnection = await wifiModule.namespace.connectSavedWifi('Office');
assert.equal(duplicateConnection, false);
assert.equal(requests.length, 1, 'a pending saved-network connection must reject duplicate clicks');
releaseRequest();
assert.equal(await firstConnection, true);
requestGate = null;

requests.length = 0;
appState.wifi = {networks: [{
  ssid: 'Office', has_psk: true, priority: 1, proxy_mode: 'proxy',
  proxy_url: 'http://user:***@proxy.example:7890', no_proxy: 'localhost',
}]};
requestResult.wifi = appState.wifi;
await wifiModule.namespace.connectSavedWifi('Office');
assert.deepEqual(
  JSON.parse(requests[0].options.body),
  {ssid: 'Office', proxy_mode: 'proxy'},
  'saved credentials and proxy secrets must remain server-side',
);

requests.length = 0;
requestResult = {
  ok: false,
  wifi: {country: 'CN', networks: [{ssid: 'Office', has_psk: true, priority: 1}]},
  wifi_status: {connected: false},
};
await wifiModule.namespace.connectSavedWifi('Office');
assert.equal(requests.length, 1);
assert.match(elements.get('wifiModal').className, /show/, 'failed saved Wi-Fi should prompt for a new password');

requests.length = 0;
wifiModule.namespace.closeWifiModal();
await wifiModule.namespace.connectSavedWifi('Guest');
assert.equal(requests.length, 0, 'unknown Wi-Fi should still wait for credentials');
assert.match(elements.get('wifiModal').className, /show/);

process.stdout.write('config web wifi tests passed\n');
