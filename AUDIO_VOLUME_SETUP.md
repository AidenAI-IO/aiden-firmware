# 音量设置脚本使用说明

## 脚本功能

`setup_audio_volume.sh` 会自动将 Luckfox Pico Zero 的音频输出音量设置到最大：
- DAC HPMIX: 2/2 (100%)
- DAC LINEOUT: 30/30 (100%)

## 部署步骤

### 1. 将脚本传输到 Pico Zero 设备

```bash
# 方法一：使用 scp
scp setup_audio_volume.sh root@<pico-zero-ip>:/root/

# 方法二：如果使用 USB 连接
scp setup_audio_volume.sh root@172.32.0.93:/root/
```

### 2. 在设备上测试脚本

SSH 连接到 Pico Zero 设备后运行：

```bash
cd /root
chmod +x setup_audio_volume.sh
./setup_audio_volume.sh
```

### 3. 设置开机自动运行

有以下几种方式：

#### 方式一：添加到 /etc/rc.local（推荐）

```bash
# 在 Pico Zero 设备上执行
vi /etc/rc.local

# 在 exit 0 之前添加：
/root/setup_audio_volume.sh

# 保存并退出
```

#### 方式二：创建 systemd 服务

```bash
# 创建服务文件
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

# 启用服务
systemctl daemon-reload
systemctl enable audio-volume.service
systemctl start audio-volume.service

# 检查状态
systemctl status audio-volume.service
```

#### 方式三：添加到应用启动脚本

如果你的应用有自己的启动脚本，可以在启动应用之前调用：

```bash
#!/bin/sh
# 设置音量
/root/setup_audio_volume.sh

# 启动应用
/path/to/your/app
```

## 验证

重启设备后，检查音量设置：

```bash
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

应该看到：
- DAC HPMIX: Mono: 2 [100%]
- DAC LINEOUT: Mono: 30 [100%]

## 手动调整音量

如果需要手动调整音量：

```bash
# 设置 DAC HPMIX (范围: 0-2)
amixer sset 'DAC HPMIX' 2

# 设置 DAC LINEOUT (范围: 0-30)
amixer sset 'DAC LINEOUT' 30

# 保存当前 ALSA 设置（可选）
alsactl store
```
