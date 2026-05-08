# Audio Volume Setup Guide

## Overview

`setup_audio_volume.sh` automatically sets the audio output volume to maximum on Luckfox Pico Zero devices:
- DAC HPMIX: 2/2 (100%)
- DAC LINEOUT: 30/30 (100%)

## Deployment Steps

### 1. Transfer Script to Pico Zero Device

```bash
# Method 1: Using scp
scp scripts/setup_audio_volume.sh root@<pico-zero-ip>:/root/

# Method 2: If using USB connection
scp scripts/setup_audio_volume.sh root@172.32.0.93:/root/
```

### 2. Test the Script on Device

After SSH into the Pico Zero device, run:

```bash
cd /root
chmod +x setup_audio_volume.sh
./setup_audio_volume.sh
```

### 3. Configure Auto-run on Startup

There are several methods:

#### Method 1: Add to /etc/rc.local (Recommended)

```bash
# On Pico Zero device
vi /etc/rc.local

# Add before exit 0:
/root/setup_audio_volume.sh

# Save and exit
```

#### Method 2: Create systemd Service

```bash
# Create service file
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

# Enable service
systemctl daemon-reload
systemctl enable audio-volume.service
systemctl start audio-volume.service

# Check status
systemctl status audio-volume.service
```

#### Method 3: Add to Application Startup Script

If your application has its own startup script, call the volume setup before starting the app:

```bash
#!/bin/sh
# Set volume
/root/setup_audio_volume.sh

# Start application
/path/to/your/app
```

## Verification

After rebooting the device, check the volume settings:

```bash
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

You should see:
- DAC HPMIX: Mono: 2 [100%]
- DAC LINEOUT: Mono: 30 [100%]

## Manual Volume Adjustment

If you need to manually adjust the volume:

```bash
# Set DAC HPMIX (range: 0-2)
amixer sset 'DAC HPMIX' 2

# Set DAC LINEOUT (range: 0-30)
amixer sset 'DAC LINEOUT' 30

# Save current ALSA settings (optional)
alsactl store
```
