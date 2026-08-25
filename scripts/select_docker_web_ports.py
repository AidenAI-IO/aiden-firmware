#!/usr/bin/env python3
"""Choose distinct host ports for the Config Web and Agent Web services."""

from __future__ import annotations

import socket
import sys


def choose_port(value: str, sockets: list[socket.socket]) -> int:
    port = int(value)
    if not 0 <= port <= 65535:
        raise ValueError(f"invalid port: {port}")
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.bind(("127.0.0.1", port))
    except OSError:
        sock.close()
        raise
    sockets.append(sock)
    return sock.getsockname()[1]


def main() -> int:
    if len(sys.argv) != 3:
        print(f"usage: {sys.argv[0]} CONFIG_PORT AGENT_PORT", file=sys.stderr)
        return 2

    sockets: list[socket.socket] = []
    try:
        config_port = choose_port(sys.argv[1], sockets)
        agent_port = choose_port(sys.argv[2], sockets)
        if config_port == agent_port:
            raise ValueError("Config Web and Agent Web ports must differ")
        print(config_port, agent_port)
        return 0
    except (OSError, ValueError) as error:
        print(f"could not select Docker Web ports: {error}", file=sys.stderr)
        return 1
    finally:
        for sock in sockets:
            sock.close()


if __name__ == "__main__":
    raise SystemExit(main())
