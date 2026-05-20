# 音量初始化与调节

`scripts/setup_audio_volume.sh` 用于在 Luckfox Pico Zero 上将音频输出相关 mixer 音量设置到最大。

## 默认设置

脚本会设置：

- `DAC HPMIX`: `2/2`（100%）
- `DAC LINEOUT`: `30/30`（100%）

## 部署脚本

```bash
scp scripts/setup_audio_volume.sh root@<device-ip>:/root/
```

登录设备后执行：

```bash
cd /root
chmod +x setup_audio_volume.sh
./setup_audio_volume.sh
```

## 开机自动执行

### 方法一：`/etc/rc.local`

在 `exit 0` 之前加入：

```sh
/root/setup_audio_volume.sh
```

### 方法二：systemd service

如果系统使用 systemd：

```bash
cat > /etc/systemd/system/audio-volume.service << 'EOF'
[Unit]
Description=Set audio volume to maximum
After=sound.target

[Service]
Type=oneshot
ExecStart=/root/setup_audio_volume.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable audio-volume.service
systemctl start audio-volume.service
systemctl status audio-volume.service
```

### 方法三：应用启动脚本

```sh
#!/bin/sh
/root/setup_audio_volume.sh
/path/to/your/app
```

## 验证

```bash
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

预期看到：

- `DAC HPMIX`: `Mono: 2 [100%]`
- `DAC LINEOUT`: `Mono: 30 [100%]`

## 手动调节

```bash
amixer sset 'DAC HPMIX' 2
amixer sset 'DAC LINEOUT' 30
alsactl store   # 可选，保存 ALSA 设置
```

## 与 audio_service 音量的关系

- mixer 音量决定底层硬件输出上限；
- `audio_service_cli set-volume --volume N` 设置服务内的逻辑音量 `0..100`；
- 如果硬件 mixer 太低，即使逻辑音量为 100，实际声音仍可能偏小。
