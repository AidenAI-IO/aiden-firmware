import json
import socket
import tempfile
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

from vphone.bridge.client import VPhoneSocketClient, VPhoneSocketError


@pytest.fixture()
def short_socket_dir():
    with tempfile.TemporaryDirectory(prefix="vp-", dir="/tmp") as directory:
        yield Path(directory)


def _serve_once(path, response: bytes, *, delay_sec: float = 0):
    ready = threading.Event()

    def serve():
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(path))
        server.listen(1)
        ready.set()
        connection, _ = server.accept()
        with connection:
            while True:
                chunk = connection.recv(4096)
                if not chunk or chunk.endswith(b"\n"):
                    break
            if delay_sec:
                time.sleep(delay_sec)
            try:
                connection.sendall(response)
            except OSError:
                pass
        server.close()

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    ready.wait(timeout=2)
    return thread


def test_client_round_trip(short_socket_dir):
    path = short_socket_dir / "v.sock"
    thread = _serve_once(path, b'{"ok":true,"screen_width":1290}\n')
    response = VPhoneSocketClient(path).request({"t": "status", "screen": False})
    thread.join(timeout=2)
    assert response["screen_width"] == 1290


def test_client_maps_remote_error(short_socket_dir):
    path = short_socket_dir / "v.sock"
    _serve_once(path, b'{"ok":false,"code":"unsupported","error":"not available"}\n')
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(path).request({"t": "keyboard_text", "text": "hello"})
    assert captured.value.code == "unsupported"


def test_client_rejects_missing_socket(short_socket_dir):
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(short_socket_dir / "missing.sock", timeout_sec=0.1).request({"t": "status"})
    assert captured.value.code == "socket_not_found"


def test_client_maps_connection_refused(short_socket_dir):
    path = short_socket_dir / "stale.sock"
    stale = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    stale.bind(str(path))
    stale.close()
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(path, timeout_sec=0.1).request({"t": "status"})
    assert captured.value.code == "socket_refused"


def test_client_rejects_oversized_response(short_socket_dir):
    path = short_socket_dir / "v.sock"
    _serve_once(path, b'{"ok":true,"data":"' + b"x" * 2048 + b'"}\n')
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(path, max_response_bytes=1024).request({"t": "status"})
    assert captured.value.code == "response_too_large"


@pytest.mark.parametrize("response,code", [(b"not-json\n", "invalid_response"), (b"", "empty_response")])
def test_client_rejects_invalid_or_empty_response(short_socket_dir, response, code):
    path = short_socket_dir / "v.sock"
    _serve_once(path, response)
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(path).request({"t": "status"})
    assert captured.value.code == code


def test_client_maps_socket_timeout(short_socket_dir):
    path = short_socket_dir / "v.sock"
    _serve_once(path, b'{"ok":true}\n', delay_sec=0.3)
    with pytest.raises(VPhoneSocketError) as captured:
        VPhoneSocketClient(path, timeout_sec=0.1).request({"t": "status"})
    assert captured.value.code == "socket_timeout"


def test_client_serializes_parallel_requests(short_socket_dir):
    client = VPhoneSocketClient(short_socket_dir / "unused.sock")
    counter_lock = threading.Lock()
    active = 0
    max_active = 0

    def fake_request(payload, *, timeout_sec=None):
        nonlocal active, max_active
        del payload, timeout_sec
        with counter_lock:
            active += 1
            max_active = max(max_active, active)
        try:
            time.sleep(0.05)
            return {"ok": True}
        finally:
            with counter_lock:
                active -= 1

    client._request = fake_request
    with ThreadPoolExecutor(max_workers=2) as executor:
        results = list(executor.map(lambda _: client.request({"t": "status"}), range(2)))
    assert results == [{"ok": True}, {"ok": True}]
    assert max_active == 1
