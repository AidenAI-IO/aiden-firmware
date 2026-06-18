# VQE Full-Duplex Implementation Summary

## Status: Stage 1 Complete (C++ audio_service)

Commit: `be00053` - feat(audio): implement AI VQE full-duplex with AEC

## What Was Implemented

### C++ Audio Service Changes

1. **AudioFrame metadata extension** (`src/aiden_sdk.h`)
   - Added `sample_rate`, `channels`, `bit_width`, `vqe_processed` fields
   - Allows VQE to communicate actual output format (mono clean PCM) vs hardware format (stereo raw)

2. **VQE state management** (`src/aiden_sdk.cpp` - AudioCaptureImpl)
   - `vqe_enabled`: parsed from `AIDEN_AUDIO_VQE` environment variable
   - `vqe_active`: tracks successful VQE initialization
   - `vqe_strict`: controls fallback behavior on init failure
   - `output_*`: actual format after VQE processing

3. **VQE initialization sequence** (`AudioCapture::init`)
   - Parse environment variables: `AIDEN_AUDIO_VQE`, `AIDEN_AUDIO_VQE_CONFIG`, `AIDEN_AUDIO_VQE_STRICT`
   - Enforce VQE requirements: 16kHz/mono/16bit
   - Set frame size to 256 samples (16ms) for VQE mode
   - Module enables for single-mic:
     - **Enabled**: `bAec`, `bFastAec`, `bAes`, `bAnr`, `bAgc`
     - **Disabled**: `bBf`, `bGsc`, `bDoa`, `bWakeup`, `bDereverb`, etc.
   - Configure VQE with `s64RefChannelType=2`, `s64RecChannelType=1`, `s64ChannelLayoutType=0x3`
   - Bind to AO device 0 / channel 0 for playback reference
   - Enable VQE before enabling AI channel
   - Graceful fallback to raw capture on init failure (non-strict mode)

4. **VQE-aware frame handling** (`src/audio_record_session.cpp`)
   - Check `frame.vqe_processed` flag to determine processing path
   - **VQE path**: Aggregate mono clean PCM to 1024-sample chunks, skip stereo downmix
   - **Raw path**: Continue stereo downmix as before
   - Log first frame format with VQE status

5. **VQE cleanup** (`AudioCapture::stop`)
   - Call `RK_MPI_AI_DisableVqe` before `DisableChn`
   - Disable I2STDM loopback mode

### Deployed Assets

1. **VQE algorithm libraries** (uclibc, ABI-matched for RV1106)
   - `overlay/oem/usr/lib/librkaudio_common.so` (116KB)
   - `overlay/oem/usr/lib/libaec_bf_process.so` (323KB)

2. **VQE configuration**
   - `overlay/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json`
   - Single-mic optimized: array modules disabled, AGC tuned for near-field

## How to Enable VQE

### Method 1: Environment variables (recommended for testing)

```bash
export AIDEN_AUDIO_VQE=1
export AIDEN_AUDIO_VQE_CONFIG=/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json
export AIDEN_AUDIO_VQE_STRICT=0
/oem/usr/bin/audio_service
```

### Method 2: Persistent config

Add to `/userdata/system/env`:
```
AIDEN_AUDIO_VQE=1
AIDEN_AUDIO_VQE_CONFIG=/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json
AIDEN_AUDIO_VQE_STRICT=0
```

### Environment Variables

| Variable | Default | Description |
|---|---:|---|
| `AIDEN_AUDIO_VQE` | `0` | Set to `1` to enable AI VQE |
| `AIDEN_AUDIO_VQE_CONFIG` | empty | Path to VQE JSON config file |
| `AIDEN_AUDIO_VQE_STRICT` | `0` | `1` = fail on VQE init error; `0` = fallback to raw capture |

## Expected Behavior

### With VQE Enabled

- **Initialization**: Log message `[AudioCapture] AI VQE enabled: config=...`
- **First frame**: `record session N first frame: sr=16000 ch=1 bw=16 vqe=1`
- **During playback**: Recorded audio contains minimal TTS echo, user voice clear
- **No playback**: Audio quality similar to raw capture

### With VQE Disabled (default)

- First frame: `sr=16000 ch=2 bw=16 vqe=0`
- Behavior identical to before VQE implementation

## Verification Steps (Stage 1)

1. **Basic audio_service test**
   ```bash
   # Terminal 1: Start service with VQE
   AIDEN_AUDIO_VQE=1 /oem/usr/bin/audio_service
   
   # Terminal 2: Record while playing
   audio_service_cli record-stream > /tmp/vqe_test.pcm &
   audio_service_cli play-stream < some_tts.pcm
   
   # Listen to /tmp/vqe_test.pcm - TTS should be significantly reduced
   ```

2. **Health check**
   ```bash
   audio_service_cli health
   # Should show: recording_active=true, playback_active=true simultaneously
   ```

3. **Log verification**
   - Look for VQE init success message
   - Check first frame format (ch=1, vqe=1)
   - No `DisableVqe` errors on stop

## Known Limitations (Stage 1)

1. **No Agent integration yet** - Full-duplex voice assistant requires Stage 2 (Agent Go changes)
2. **Format restriction** - VQE only works with 16kHz/mono/16bit
3. **AO binding timing** - If AO not started, `SetVqeAttr` may fail (non-strict mode continues)
4. **No runtime tuning** - VQE parameters require service restart to change

## Next Steps (Stage 2 - Agent)

Per `VQE_全双工实现方案.md`, Agent changes needed:

1. Add `voice_full_duplex_enabled` config flag (default: `false`)
2. Modify `runVoiceSession` to keep recording session open during TTS
3. Skip `StopRecording()` before TTS when full-duplex enabled
4. Ensure session cleanup on exit

Stage 2 will enable true "speak while listening" behavior in the voice assistant.

## Technical Notes

### AEC Reference Signal Path

```
TTS PCM → AudioPlayer → RK_MPI_AO_SendFrame → speaker
                                               ↓
                          I2STDM Mode2 hardware loopback
                                               ↓
mic ──────────────────────────────────────→ [merged]
                                               ↓
                          RK_MPI_AI + AI VQE (AEC+ANR+AGC)
                                               ↓
                          clean mono PCM → AudioRecordSession
```

### VQE Processing Chain

For single-mic configuration:
1. **AEC** (Acoustic Echo Cancellation) - removes speaker echo using reference
2. **FastAEC** - lighter AEC variant for near-field
3. **AES** (Acoustic Echo Suppression) - residual echo suppression
4. **ANR** (Acoustic Noise Reduction) - stationary noise removal
5. **AGC** (Automatic Gain Control) - normalize output level

### CPU Considerations

VQE processing on single-core Cortex-A7:
- Measured overhead: TBD (needs on-board profiling)
- If CPU cannot keep up: disable modules in order: AES → AGC → ANR → keep only AEC

## Files Modified

```
 src/aiden_sdk.h                          |   5 +
 src/aiden_sdk.cpp                        | 146 ++++++++++-
 src/audio_record_session.cpp             |  47 ++++
 overlay/oem/usr/lib/librkaudio_common.so | Bin (116KB)
 overlay/oem/usr/lib/libaec_bf_process.so | Bin (323KB)
 overlay/oem/usr/share/aiden/vqe/config_aivqe_aiden_singlemic.json | 166 lines
```

## References

- Implementation plan: `VQE_全双工实现方案.md`
- Commit: `be00053`
- Rockchip AI VQE SDK: `pico-sdk/media/common_algorithm/audio/rkaudio_algorithms/`
