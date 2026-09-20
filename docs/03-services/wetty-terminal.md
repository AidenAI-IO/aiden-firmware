---
sidebar_position: 3
---

# ttyd Browser Terminal

The Debian firmware image integrates ttyd as a browser terminal for board-side
maintenance. The board-side public URL is
`http://192.168.42.1:3000/webtty/`.

## Debian Integration

The production build downloads the pinned static ttyd 1.7.3 armhf binary,
verifies its SHA-256 digest, and installs it in the business package without adding a
Node.js runtime. Build it with:

```bash
./debian_build.sh
```

## Startup

ttyd is managed by systemd:

```bash
systemctl start aiden-ttyd.service
systemctl status aiden-ttyd.service --no-pager
systemctl restart aiden-ttyd.service
```

The service reads `/etc/aiden_boot.conf`. Set `ENABLE_TTYD=0` to disable
startup. Existing `ENABLE_WETTY`, `WETTY_*`, and `WETTY_COMMAND` overrides are
accepted as migration fallbacks.

Default runtime values:

| Parameter | Default |
| --- | --- |
| Listen interface | all interfaces |
| Port | `3000` |
| Base path | `/webtty/` |
| Command | `/usr/lib/aiden/aiden-ttyd-login` |
| Log | `/var/log/ttyd/ttyd.log` |

## Access

The config web page at `http://192.168.42.1` includes a `Terminal` link. On the
board it opens ttyd directly:

```text
http://192.168.42.1:3000/webtty/
```

Agent Web and the Docker sandbox also proxy `/webtty/` to ttyd on their
published Agent Web port.

The ttyd daemon runs as the unprivileged `aiden` user. Its default login helper
prompts for a local account name, then uses `su --login` to authenticate that
account's password. This avoids a Debian `login`/PTY interaction that leaves
ttyd waiting for input without displaying a prompt. A custom `TTYD_COMMAND`
still runs as `aiden`.

## Mobile browser defaults

ttyd 1.7.3 does not include a mobile virtual-keyboard toolbar, but its
`--client-option` mechanism tunes the bundled xterm.js client. Aiden applies
these defaults for the small touch screen and for lower-end mobile browsers:

| Option | Default | Purpose |
| --- | --- | --- |
| `rendererType` | `canvas` | Avoid requiring a WebGL context on mobile browsers |
| `fontSize` | `24` | Keep text readable and avoid iOS input auto-zoom |
| `scrollback` | `500` | Bound the browser-side terminal buffer |
| `cursorStyle` | `bar` | Make the insertion point easier to follow while typing |
| `disableResizeOverlay` | `true` | Avoid transient overlays when mobile browser chrome resizes the viewport |
| `max-clients` | `2` | Bound concurrent shells on the memory-constrained board |

Set the corresponding `TTYD_*` variables in `/etc/aiden_boot.conf` to adjust
these values. The legacy `WETTY_*` names remain accepted for migration. The
mobile toolbar and viewport metadata documented by newer ttyd releases are not
available in the pinned 1.7.3 client.
