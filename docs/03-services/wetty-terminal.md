---
sidebar_position: 3
---

# WeTTY Browser Terminal

The firmware image integrates WeTTY as an optional browser terminal for board-side maintenance.

## Buildroot Integration

The active Pico Zero Buildroot defconfig enables:

- `BR2_PACKAGE_NODEJS=y`
- `BR2_PACKAGE_OPENSSL=y`
- `BR2_PACKAGE_NODEJS_MODULES_ADDITIONAL="--omit=optional wetty@2.5.0 sass@1.69.7"`

The SDK-provided Buildroot tree is `2023.02.6` and builds Node.js `16.20.0`. WeTTY `2.5.0` is pinned because later WeTTY releases require Node.js `>=18`, and `sass@1.69.7` is pinned to avoid Node.js 20-only transitive releases. Optional npm dependencies are omitted so prebuilt non-ARM native addons are not copied into the target rootfs.

Run the Linux/image build from an x86 host:

```bash
./build.sh firmware
```

The `sysdrv` stage builds Node.js and installs the pinned WeTTY npm module into the target rootfs.

## Startup

WeTTY is managed by:

```bash
/etc/init.d/S57wetty start
/etc/init.d/S57wetty status
/etc/init.d/S57wetty restart
```

The service reads `/etc/aiden_boot.conf`. Set `ENABLE_WETTY=0` to disable startup.

Default runtime values:

| Parameter | Default |
| --- | --- |
| Listen host | `0.0.0.0` |
| Port | `3000` |
| Base path | `/wetty/` |
| Command | `/bin/login` |
| Log | `/var/log/wetty/wetty.log` |

## Access

The config web page at `http://192.168.42.1` includes a `Terminal` link. The link uses the Agent Web reverse proxy so the same page also works in the Docker sandbox:

```text
http://192.168.42.1:8080/wetty/
```

WeTTY itself still listens on port `3000`; the Agent Web service proxies `/wetty/` to it.

The init script uses `/bin/login`, so authenticate with the board's Linux account credentials.
