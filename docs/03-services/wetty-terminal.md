---
sidebar_position: 3
---

# ttyd Browser Terminal

The firmware image integrates ttyd as an optional browser terminal for board-side maintenance. The public `/wetty/` path is retained for compatibility with existing Agent Web links.

## Buildroot Integration

The active Pico Zero Buildroot defconfig enables:

- `BR2_PACKAGE_TTYD=y`
- `BR2_PACKAGE_OPENSSL=y`

The SDK-provided Buildroot tree is `2023.02.6` and includes ttyd `1.7.3`. Buildroot selects ttyd's `libuv`, `libwebsockets`, `json-c`, OpenSSL, and zlib dependencies. This replaces the Node.js runtime and npm module tree with a single native binary.

Run the Linux/image build from an x86 host:

```bash
./build_image.sh
```

The `sysdrv` stage builds and installs the ttyd package into the target rootfs.

## Startup

ttyd is managed by:

```bash
/etc/init.d/S57ttyd start
/etc/init.d/S57ttyd status
/etc/init.d/S57ttyd restart
```

The service reads `/etc/aiden_boot.conf`. Set `ENABLE_TTYD=0` to disable startup. Existing `ENABLE_WETTY`, `WETTY_*`, and `WETTY_COMMAND` overrides are accepted as migration fallbacks.

Default runtime values:

| Parameter | Default |
| --- | --- |
| Listen interface | all interfaces |
| Port | `3000` |
| Base path | `/wetty/` |
| Command | `/bin/login` |
| Log | `/var/log/ttyd/ttyd.log` |

## Access

The config web page at `http://192.168.42.1` includes a `Terminal` link. The link uses the Agent Web reverse proxy so the same page also works in the Docker sandbox:

```text
http://192.168.42.1:8080/wetty/
```

ttyd itself still listens on port `3000`; the Agent Web service proxies `/wetty/` to it.

The init script uses `/bin/login`, so authenticate with the board's Linux account credentials.

## Mobile browser defaults

ttyd 1.7.3 does not include a mobile virtual-keyboard toolbar, but its
`--client-option` mechanism can tune the bundled xterm.js client. Aiden applies
these defaults for the small touch screen and for lower-end mobile browsers:

| Option | Default | Purpose |
| --- | --- | --- |
| `rendererType` | `canvas` | Avoid requiring a WebGL context on mobile browsers |
| `fontSize` | `16` | Keep text readable and avoid iOS input auto-zoom |
| `scrollback` | `500` | Bound the browser-side terminal buffer |
| `cursorStyle` | `bar` | Make the insertion point easier to follow while typing |
| `disableResizeOverlay` | `true` | Avoid transient overlays when mobile browser chrome resizes the viewport |
| `max-clients` | `2` | Bound concurrent shells on the memory-constrained board |

Set the corresponding `TTYD_*` variables in `/etc/aiden_boot.conf` to adjust
these values. The legacy `WETTY_*` names remain accepted for migration. The
mobile toolbar and viewport metadata documented by newer ttyd releases are not
available in the Buildroot-provided 1.7.3 client.
