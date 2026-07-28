# WeTTY Browser Terminal

The firmware image integrates WeTTY as an optional browser terminal for board-side maintenance.

## Buildroot Integration

The active Pico Zero Buildroot defconfig enables:

- `BR2_PACKAGE_NODEJS=y`
- `BR2_PACKAGE_OPENSSL=y`
- `BR2_PACKAGE_NODEJS_MODULES_ADDITIONAL="wetty@2.7.0"`

The SDK-provided Buildroot tree is `2023.02.6` and builds Node.js `16.20.0`. WeTTY `2.7.0` is pinned because current WeTTY 3.x releases require Node.js `>=20`.

Run the Linux/image build from an x86 host:

```bash
./build_image.sh
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
| Host | `192.168.42.1` |
| Port | `3000` |
| Base path | `/wetty/` |
| Command | `/bin/login` |
| Log | `/var/log/wetty/wetty.log` |

## Access

The config web page at `http://192.168.42.1` includes a `Terminal` link. It opens:

```text
http://192.168.42.1:3000/wetty/
```

The init script uses `/bin/login`, so authenticate with the board's Linux account credentials.
