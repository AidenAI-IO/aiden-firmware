import {appState, byId, runtimeFunction} from './state.js';

const t = runtimeFunction('t');

function renderMessage(message) {
  if (!message) return '';
  if (message.parts) return message.parts.map(renderMessage).join('');
  if (message.key) return t(message.key, message.params || {});
  return String(message.raw || '');
}

function renderTestToast() {
  const state = appState.testToast;
  if (!state.owner || !state.view) return;
  const toast = byId('testToast');
  if (toast) toast.className = state.view.className;
  const title = byId('testToastTitle');
  if (title) title.textContent = renderMessage(state.view.title);
  const body = byId('testToastBody');
  if (body) body.textContent = renderMessage(state.view.body);
}

function beginTestToast(owner, view) {
  const state = appState.testToast;
  state.generation++;
  state.owner = owner;
  state.view = view;
  const token = {owner, generation: state.generation};
  renderTestToast();
  return token;
}

function updateTestToast(token, view) {
  const state = appState.testToast;
  if (!token || state.owner !== token.owner || state.generation !== token.generation) return false;
  state.view = view;
  renderTestToast();
  return true;
}

function clearTestToast() {
  const state = appState.testToast;
  state.generation++;
  state.owner = null;
  state.view = null;
  const toast = byId('testToast');
  if (toast) toast.className = 'test-toast';
}

document.addEventListener('aiden:locale-changed', renderTestToast);

export {beginTestToast, clearTestToast, renderTestToast, updateTestToast};
