---
sidebar_position: 3
---

# WeTTY Browser Terminal

The firmware image integrates WeTTY as an optional browser terminal for board-side maintenance.

## Debian Integration

The production Debian image deliberately does not install WeTTY or Node.js.
It ships a disabled `aiden-wetty.service` so a separately reviewed diagnostic
image can add `/usr/bin/wetty` without introducing another service definition.
The unit also checks `ENABLE_WETTY` in `/etc/aiden_boot.conf`.

Build the normal production image with:

```bash
./debian_build.sh
```

That image will leave the optional unit inactive because `/usr/bin/wetty` is
absent. A diagnostic image must install a Debian-compatible WeTTY executable,
enable `aiden-wetty.service`, and pass its own security review.

## Startup

On such a diagnostic image, WeTTY is managed by:

```bash
systemctl start aiden-wetty.service
systemctl status aiden-wetty.service --no-pager
systemctl restart aiden-wetty.service
```

Set `ENABLE_WETTY=0` to prevent startup even when the unit is enabled.

Default runtime values:

| Parameter | Default |
| --- | --- |
| Listen host | `192.168.42.1` |
| Port | `3000` |
| Base path | `/wetty/` |
| Command | `/bin/login` |
| Log | `/var/log/wetty/wetty.log` |

## Access

When installed, the Agent Web reverse proxy exposes the terminal at:

```text
http://192.168.42.1:8080/wetty/
```

WeTTY itself still listens on port `3000`; the Agent Web service proxies `/wetty/` to it.

The init script uses `/bin/login`, so authenticate with the board's Linux account credentials.
