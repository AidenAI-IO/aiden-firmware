"""The experimental stop hook must cancel only its own request and exit cleanly."""
import importlib.util
import json
from pathlib import Path
import threading

import pytest

from runner.agent_client import AgentClient


@pytest.fixture
def strict_runner(monkeypatch):
    monkeypatch.setattr(AgentClient, '_wait_for_chat_result', AgentClient._wait_for_chat_result)
    path = Path(__file__).resolve().parents[1]/'scripts/pr715/strict_runner.py'
    spec = importlib.util.spec_from_file_location('pr715_strict_runner_test', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_terminal_cancels_owned_request_and_saves_evidence(strict_runner, monkeypatch, tmp_path):
    path = tmp_path/'trial.terminal.json'
    monkeypatch.setenv('PR715_TERMINAL_FILE', str(path))
    canceled = threading.Event()

    class Client:
        calls = []

        def cancel_chat(self, request_id):
            self.calls.append(request_id)
            canceled.set()
            return 'canceled'

    def wait(client, request_id, timeout_sec):
        path.write_text(json.dumps({'reason':'target_overshoot'}))
        assert canceled.wait(2)
        raise RuntimeError('request canceled')

    monkeypatch.setattr(strict_runner, 'original_wait', wait)
    client = Client()
    with pytest.raises(RuntimeError, match='request canceled'):
        strict_runner.wait_with_terminal(client, 'owned-request', 2)
    assert client.calls == ['owned-request']
    evidence = json.loads(path.with_suffix('.cancel.json').read_text())
    assert evidence['request_id'] == 'owned-request'
    assert evidence['status'] == 'canceled'


def test_normal_completion_is_not_canceled(strict_runner, monkeypatch, tmp_path):
    monkeypatch.setenv('PR715_TERMINAL_FILE', str(tmp_path/'absent.terminal.json'))
    monkeypatch.setattr(strict_runner, 'original_wait', lambda *_: 'normal result')

    class Client:
        def cancel_chat(self, request_id):
            pytest.fail('normal completion must not be canceled')

    assert strict_runner.wait_with_terminal(Client(), 'normal-request', 2) == 'normal result'
