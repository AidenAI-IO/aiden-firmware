import {appState, byId, registerRuntime, runtimeFunction} from './state.js';
import {beginTestToast, updateTestToast} from './test-toast.js';

const request = runtimeFunction('request');
const agentRequest = runtimeFunction('agentRequest');
const readSection = runtimeFunction('readSection');
const t = runtimeFunction('t');

function sttResultBody(payload) {
  const parts = [];
  if (payload.transcript) {
    parts.push({key: 'stt.result'});
    parts.push({raw: '\n' + payload.transcript + '\n\n'});
  }
  (payload.results || []).forEach(function(result) {
    parts.push({raw: (result.passed ? '\u2705' : '\u274C') + ' ' + result.check + ': ' + result.detail + '\n'});
  });
  return parts.length ? {parts} : {key: 'stt.no_result'};
}

function setSTTTestButtonState(recording, busy) {
  appState.sttTest.recording = !!recording;
  appState.sttTest.busy = !!busy;
  const btn = byId('test-stt');
  if (!btn) return;
  btn.disabled = !!busy;
  btn.textContent = busy
    ? t(recording ? 'stt.ending' : 'stt.starting')
    : t(recording ? 'stt.end_recording' : 'action.test');
  btn.className = 'button ' + (recording ? 'primary' : 'secondary');
}

async function startSTTTest() {
  if (appState.sttTest.recording || appState.sttTest.busy) return;
  setSTTTestButtonState(false, true);
  const token = beginTestToast('stt', {
    className: 'test-toast show',
    title: {key: 'stt.activating'},
    body: {key: 'stt.start_speaking'},
  });
  try {
    const payload = await agentRequest('/api/config/test/stt/start', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({stt_values: readSection('stt'), audio_values: readSection('audio')}),
    });
    setSTTTestButtonState(true, false);
    const bodyParts = [];
    if (payload.sample_rate) bodyParts.push({raw: 'sample_rate: ' + payload.sample_rate + '\n'});
    bodyParts.push({key: 'stt.keep_speaking'});
    updateTestToast(token, {
      className: 'test-toast show',
      title: {key: 'stt.recording'},
      body: {parts: bodyParts},
    });
  } catch (err) {
    setSTTTestButtonState(false, false);
    updateTestToast(token, {
      className: 'test-toast show fail',
      title: {key: 'stt.start_failed'},
      body: {raw: err.message},
    });
  }
}

async function stopSTTTest() {
  if (!appState.sttTest.recording || appState.sttTest.busy) return;
  setSTTTestButtonState(true, true);
  const token = beginTestToast('stt', {
    className: 'test-toast show',
    title: {key: 'stt.recognizing'},
    body: {key: 'stt.waiting_result'},
  });
  try {
    const payload = await agentRequest('/api/config/test/stt/stop', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: '{}',
    });
    setSTTTestButtonState(false, false);
    updateTestToast(token, {
      className: 'test-toast show ' + (payload.ok ? 'pass' : 'fail'),
      title: {key: payload.ok ? 'stt.completed' : 'stt.failed'},
      body: sttResultBody(payload),
    });
  } catch (err) {
    setSTTTestButtonState(true, false);
    updateTestToast(token, {
      className: 'test-toast show fail',
      title: {key: 'stt.stop_failed'},
      body: {raw: err.message},
    });
  }
}

async function toggleSTTTest() {
  if (appState.sttTest.recording) await stopSTTTest();
  else await startSTTTest();
}

document.addEventListener('aiden:locale-changed', function() {
  setSTTTestButtonState(appState.sttTest.recording, appState.sttTest.busy);
});

export {setSTTTestButtonState, startSTTTest, stopSTTTest, toggleSTTTest};
registerRuntime({setSTTTestButtonState, startSTTTest, stopSTTTest, toggleSTTTest});
