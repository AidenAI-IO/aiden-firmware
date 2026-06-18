# VQE Full-Duplex Testing Guide

## Quick Start

### 1. Deploy to Device

Ensure the build output includes:
- `build/bin/audio_service` with VQE support
- `overlay/oem/usr/lib/librkaudio_common.so`
- `overlay/oem/usr/lib/libaec_bf_process.so`
- `overlay/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json`

### 2. Enable VQE (SSH to device)

```bash
# Add to /userdata/system/env
echo "AIDEN_AUDIO_VQE=1" >> /userdata/system/env
echo "AIDEN_AUDIO_VQE_CONFIG=/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json" >> /userdata/system/env
echo "AIDEN_AUDIO_VQE_STRICT=0" >> /userdata/system/env

# Restart audio service
killall audio_service
/etc/init.d/S53audio_service restart
```

### 3. Verify VQE Initialization

```bash
# Check logs for VQE enable message
logread | grep "AI VQE enabled"
# Expected: [AudioCapture] AI VQE enabled: config=/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json, modules=[aec,fast_aec,aes,anr,agc]

# Check first frame format
logread | grep "first frame"
# Expected: record session X first frame: sr=16000 ch=1 bw=16 vqe=1
```

## Test Scenarios

### Test 1: Raw Capture Baseline (VQE Off)

```bash
# Disable VQE
sed -i 's/AIDEN_AUDIO_VQE=1/AIDEN_AUDIO_VQE=0/' /userdata/system/env
killall audio_service && /etc/init.d/S53audio_service restart

# Record 10 seconds of silence
audio_service_cli record-stream -d 10 > /tmp/baseline_silence.pcm

# Record while speaking
audio_service_cli record-stream -d 10 > /tmp/baseline_speech.pcm
# (speak into mic during recording)
```

### Test 2: VQE Capture (No Playback)

```bash
# Enable VQE
sed -i 's/AIDEN_AUDIO_VQE=0/AIDEN_AUDIO_VQE=1/' /userdata/system/env
killall audio_service && /etc/init.d/S53audio_service restart

# Record with VQE
audio_service_cli record-stream -d 10 > /tmp/vqe_noplay.pcm
# (speak into mic during recording)
```

### Test 3: VQE with Playback (AEC Test)

```bash
# Prepare test audio (TTS or music)
# Transfer to device as /tmp/playback_test.pcm (16kHz mono PCM16)

# Terminal 1: Start recording
audio_service_cli record-stream -d 15 > /tmp/vqe_with_echo.pcm

# Terminal 2: Start playback (after 2 seconds)
sleep 2 && audio_service_cli play-stream < /tmp/playback_test.pcm

# Expected result: /tmp/vqe_with_echo.pcm contains:
# - First 2s: silence or speech (no playback)
# - Middle: playback is significantly attenuated
# - End: return to clear recording
```

### Test 4: Concurrent Sessions

```bash
# Check health before
audio_service_cli health
# recording_active=false, playback_active=false

# Terminal 1
audio_service_cli record-stream > /tmp/concurrent.pcm &
RECORD_PID=$!

# Terminal 2
audio_service_cli play-stream < /tmp/playback_test.pcm &
PLAY_PID=$!

# Check health during
audio_service_cli health
# Expected: recording_active=true, playback_active=true

# Wait and stop
wait $PLAY_PID
kill $RECORD_PID

# Check health after
audio_service_cli health
# recording_active=false, playback_active=false
```

## Offline Analysis

Transfer captured PCM files to PC for analysis:

```bash
# From device
scp /tmp/*.pcm user@pc:/tmp/

# On PC with ffplay/audacity
ffplay -f s16le -ar 16000 -ac 1 /tmp/baseline_speech.pcm
ffplay -f s16le -ar 16000 -ac 1 /tmp/vqe_with_echo.pcm

# Compare waveforms in Audacity
# Import as: signed 16-bit PCM, 16000Hz, mono
```

### Expected Observations

1. **baseline_silence.pcm**: Background noise level
2. **vqe_noplay.pcm**: Similar to baseline, possibly slightly cleaner (ANR/AGC effect)
3. **vqe_with_echo.pcm**: 
   - Playback portion should be 10-20dB quieter than raw capture
   - User speech should remain clear
   - No self-oscillation/howling

## Troubleshooting

### VQE Not Initializing

```bash
# Check library presence
ls -l /oem/usr/lib/librkaudio_common.so /oem/usr/lib/libaec_bf_process.so

# Check config file
cat /oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json

# Check logs for error
logread | grep -i vqe
# Look for: SetVqeModuleEnable failed, SetVqeAttr failed, EnableVqe failed
```

### VQE Initialized but No Effect

```bash
# Verify first frame shows vqe=1
logread | grep "first frame"

# Check if playback is actually running
audio_service_cli health
```

### Audio Distortion / Artifacts

Possible causes:
- CPU overload: Check `top` during recording
- VQE config mismatch: Verify 16kHz/mono/256-sample
- AGC over-amplifying: Try disabling AGC in config

### AO Binding Failure

```bash
# If seeing "SetVqeAttr failed" but non-strict mode:
# VQE should continue with raw capture fallback

# To test strict mode:
echo "AIDEN_AUDIO_VQE_STRICT=1" >> /userdata/system/env
# Now audio_service should fail to start if VQE cannot init
```

## Performance Monitoring

```bash
# CPU usage during VQE recording
top -b -d 1 | grep audio_service

# Expected: 20-40% CPU on single-core A7 (needs real measurement)

# Memory usage
free -m
ps aux | grep audio_service
```

## Success Criteria

### Stage 1 (C++ audio_service only)

- [x] VQE initializes without crash
- [x] First frame logs show vqe=1, ch=1
- [x] Recording and playback can be concurrent
- [ ] Playback echo reduced by >10dB (verify on-board)
- [ ] User speech remains intelligible (verify on-board)
- [ ] CPU usage <50% (verify on-board)

### Stage 2 (After Agent integration)

- [ ] Voice assistant continues listening during TTS
- [ ] User can interrupt TTS with voice
- [ ] VAD not triggered by residual echo
- [ ] No recording session leaks

## Cleanup

```bash
# Disable VQE
sed -i '/AIDEN_AUDIO_VQE/d' /userdata/system/env

# Restart service
killall audio_service && /etc/init.d/S53audio_service restart

# Remove test files
rm /tmp/*.pcm
```

## Next Steps

After verifying Stage 1 success:
1. Measure actual CPU/memory overhead
2. Tune AGC parameters if output too quiet/loud
3. Proceed to Stage 2: Agent Go changes for full-duplex voice assistant

## References

- Implementation summary: `docs/03-services/vqe-implementation.md`
- Design document: `VQE_全双工实现方案.md`
- Commit: `be00053`
