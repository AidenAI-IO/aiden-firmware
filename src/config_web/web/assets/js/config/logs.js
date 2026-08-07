import {appState, byId, registerRuntime, runtimeFunction} from './state.js';

const request = runtimeFunction('request');
const setBanner = runtimeFunction('setBanner');
const setDetails = runtimeFunction('setDetails');
const t = runtimeFunction('t');
let agentLogRefreshId = 0;

async function exportLogs() {
  const btn = byId('exportLogsBtn');
  if (btn) btn.disabled = true;
  setBanner(t('logs.exporting'), false);
  setDetails('');
  try {
    const res = await fetch('/api/logs/export');
    if (!res.ok) {
      const text = await res.text();
      let msg = text || ('HTTP ' + res.status);
      try {
        const body = JSON.parse(text);
        msg = body.error || msg;
      } catch (_err) {
      }
      throw new Error(msg);
    }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'aiden-logs.tar.gz';
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
    setBanner(t('logs.exported'), false);
  } catch (err) {
    setBanner(t('logs.export_failed'), true);
    setDetails(err.message);
  } finally {
    if (btn) btn.disabled = false;
  }
}

function formatLogSize(bytes) {
  bytes = Number(bytes || 0);
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / 1048576).toFixed(1) + ' MB';
}

function classifyLine(line) {
  const text = String(line || '');
  const severity = text.match(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \[(DEBUG|INFO|WARN|ERROR)\](?:\s|$)/);
  if (severity) {
    if (severity[1] === 'ERROR') return 'log-error';
    if (severity[1] === 'WARN') return 'log-warn';
    return '';
  }
  const lower = text.toLowerCase();
  if (lower.indexOf('failed') >= 0 || lower.indexOf('error') >= 0 || lower.indexOf('pq:') >= 0 || lower.indexOf('panic') >= 0) return 'log-error';
  if (lower.indexOf('fallback') >= 0 || lower.indexOf('not found') >= 0 || lower.indexOf('skip') >= 0 || lower.indexOf('warn') >= 0) return 'log-warn';
  if (lower.indexOf('[update]') >= 0) return 'log-update';
  if (lower.indexOf('[proxy]') >= 0) return 'log-proxy';
  if (lower.indexOf('cache hit') >= 0) return 'log-cache';
  if (lower.indexOf('[gin]') >= 0 || /\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/i.test(text)) return 'log-http';
  if (lower.indexOf('page created') >= 0 || lower.indexOf('html updated') >= 0 || lower.indexOf('new resource') >= 0) return 'log-success';
  return '';
}

function renderLogText(el, text, emptyText) {
  if (!el) return;
  const content = text ? String(text) : String(emptyText || '');
  const fragment = document.createDocumentFragment();
  el.textContent = '';
  content.split(/\r?\n/).forEach(function(line) {
    const span = document.createElement('span');
    span.className = 'log-line ' + classifyLine(line);
    span.textContent = line;
    fragment.appendChild(span);
  });
  el.appendChild(fragment);
}

function agentLogBodyText(snapshot) {
  snapshot = snapshot || {};
  const text = snapshot.log || '';
  return text ? String(text) : (snapshot.exists ? '\u0000empty' : '\u0000unavailable');
}

function agentLogBodyEquals(a, b) {
  return agentLogBodyText(a) === agentLogBodyText(b);
}

function agentLogSelectionActive(el) {
  if (!el || !document.getSelection) return false;
  const selection = document.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return false;
  for (let i = 0; i < selection.rangeCount; i++) {
    const range = selection.getRangeAt(i);
    const node = range.commonAncestorContainer;
    if (node && (el === node || el.contains(node))) return true;
  }
  return false;
}

function isAgentLogAtBottom(el) {
  if (!el) return true;
  return (el.scrollTop + el.clientHeight) >= (el.scrollHeight - 16);
}

function updateAgentLogAutoScrollButton() {
  const btn = byId('autoScrollAgentLogBtn');
  if (btn) {
    btn.textContent = t(appState.agentLogAutoScroll ? 'action.auto_scroll' : 'logs.jump_to_bottom');
    btn.className = 'button ' + (appState.agentLogAutoScroll ? 'primary' : 'ghost');
  }
  const refresh = byId('refreshAgentLogBtn');
  if (refresh) refresh.className = 'button ' + (appState.agentLogAutoScroll ? 'primary' : 'ghost');
}

function setAgentLogAutoScroll(enabled) {
  appState.agentLogAutoScroll = !!enabled;
  updateAgentLogAutoScrollButton();
}

function syncAgentLogAutoScroll() {
  setAgentLogAutoScroll(isAgentLogAtBottom(byId('agentLogText')));
}

function renderAgentLogMeta(snapshot) {
  snapshot = snapshot || {};
  const meta = [];
  if (snapshot.path) meta.push(snapshot.path);
  meta.push(formatLogSize(snapshot.size_bytes));
  if (snapshot.truncated) meta.push(t('logs.showing_latest'));
  const error = snapshot.error || appState.agentLogBackgroundError;
  if (error) meta.push(error);
  const el = byId('agentLogMeta');
  if (el) el.textContent = meta.filter(Boolean).join(' · ');
}

function renderAgentLogBody(snapshot, preserveScroll) {
  const textEl = byId('agentLogText');
  if (!textEl) return;
  textEl.removeAttribute('data-i18n');
  const previousScrollTop = textEl.scrollTop;
  renderLogText(textEl, snapshot.log || '', t(snapshot.exists ? 'logs.empty' : 'logs.unavailable'));
  if (appState.agentLogAutoScroll) textEl.scrollTop = textEl.scrollHeight;
  else if (preserveScroll) textEl.scrollTop = previousScrollTop;
}

function renderAgentLogFailure(view) {
  const textEl = byId('agentLogText');
  if (textEl) textEl.removeAttribute('data-i18n');
  renderLogText(textEl, '', t(view.key, {error: view.error}));
  const meta = byId('agentLogMeta');
  if (meta) meta.textContent = '';
}

function renderAgentLog(snapshot, forceBodyRender) {
  snapshot = snapshot || {};
  const hadPrevious = appState.agentLog !== null;
  const previous = appState.agentLog || {};
  appState.agentLog = snapshot;
  const contentChanged = !!forceBodyRender || !hadPrevious || !agentLogBodyEquals(previous, snapshot);
  renderAgentLogMeta(snapshot);
  if (!contentChanged) return;
  const textEl = byId('agentLogText');
  const wasAtBottom = isAgentLogAtBottom(textEl);
  if (wasAtBottom) setAgentLogAutoScroll(true);
  renderAgentLogBody(snapshot, true);
  syncAgentLogAutoScroll();
}

function applyPendingAgentLogSnapshotIfIdle() {
  if (appState.agentLogFailureView) return;
  const snapshot = appState.agentLogPendingSnapshot;
  if (!snapshot) return;
  const textEl = byId('agentLogText');
  if (agentLogSelectionActive(textEl)) return;
  appState.agentLogPendingSnapshot = null;
  renderAgentLog(snapshot);
}

async function refreshAgentLog(showBanner) {
  const refreshId = ++agentLogRefreshId;
  const btn = byId('refreshAgentLogBtn');
  if (btn) btn.disabled = true;
  try {
    const payload = await request('/api/agent/logs', {method: 'GET'});
    if (refreshId !== agentLogRefreshId) return;
    const snapshot = payload.agent_log || {};
    const recoveringFromFailure = !!appState.agentLogFailureView;
    appState.agentLogFailureView = null;
    appState.agentLogBackgroundError = null;
    if (!recoveringFromFailure && !showBanner && agentLogSelectionActive(byId('agentLogText'))) {
      renderAgentLogMeta(snapshot);
      if (!agentLogBodyEquals(appState.agentLog, snapshot)) appState.agentLogPendingSnapshot = snapshot;
      else {
        appState.agentLogPendingSnapshot = null;
        appState.agentLog = snapshot;
      }
      return;
    }
    appState.agentLogPendingSnapshot = null;
    renderAgentLog(snapshot, recoveringFromFailure);
    if (showBanner) setBanner(t('logs.refreshed'), false);
  } catch (err) {
    if (refreshId !== agentLogRefreshId) return;
    if (showBanner) {
      appState.agentLogPendingSnapshot = null;
      appState.agentLogBackgroundError = null;
      appState.agentLogFailureView = {key: 'logs.load_failed', error: err.message};
      renderAgentLogFailure(appState.agentLogFailureView);
      setBanner(t('logs.refresh_failed'), true);
      setDetails(err.message);
    } else {
      appState.agentLogBackgroundError = err.message;
      renderAgentLogMeta(appState.agentLogPendingSnapshot || appState.agentLog || {});
    }
  } finally {
    if (btn && refreshId === agentLogRefreshId) btn.disabled = false;
    updateAgentLogAutoScrollButton();
  }
}

function toggleAgentLogAutoScroll() {
  const textEl = byId('agentLogText');
  setAgentLogAutoScroll(!appState.agentLogAutoScroll);
  if (appState.agentLogAutoScroll && textEl) {
    textEl.scrollTop = textEl.scrollHeight;
    refreshAgentLog(false);
  }
}

document.addEventListener('aiden:locale-changed', function() {
  updateAgentLogAutoScrollButton();
  if (appState.agentLogFailureView) {
    renderAgentLogFailure(appState.agentLogFailureView);
    return;
  }
  const snapshot = appState.agentLogPendingSnapshot || appState.agentLog;
  if (!snapshot) return;
  renderAgentLogMeta(snapshot);
  if (appState.agentLogPendingSnapshot) return;
  if (!snapshot.log) renderAgentLogBody(snapshot, true);
});

export {
  exportLogs, formatLogSize, renderLogText, applyPendingAgentLogSnapshotIfIdle,
  refreshAgentLog, toggleAgentLogAutoScroll, syncAgentLogAutoScroll, setAgentLogAutoScroll,
};
registerRuntime({
  exportLogs, formatLogSize, renderLogText, applyPendingAgentLogSnapshotIfIdle,
  refreshAgentLog, toggleAgentLogAutoScroll, syncAgentLogAutoScroll, setAgentLogAutoScroll,
});
