# 720p Compatibility Fix for Pixel 8 and Similar Devices

## Problem

The `touch_gesture` tool was failing on Pixel 8 with the error:
```
error: touch_gesture completed with output "ok", but stable-screen wait failed: 
error: frame service: SERVICE_RECOVERING
```

**Root Cause**: Pixel 8 only supports HDMI output up to 720p (1280x720), but the system was defaulting to 1080p (1920x1080) with a 1080p30-only EDID. This caused:
- Frame capture initialization to fail
- Frame service to enter `SERVICE_RECOVERING` state repeatedly
- `stable-screen` wait to timeout with error

## Solution

Modified the default configuration to use 720p for better device compatibility:

### Changes Made

1. **Default Resolution Changed to 720p** (`src/camera_frame_utils.h`)
   - Changed default from `1920x1080` to `1280x720`
   - Added environment variable support:
     - `AIDEN_DEFAULT_WIDTH` - override default width
     - `AIDEN_DEFAULT_HEIGHT` - override default height
     - `AIDEN_DEFAULT_EDID` - override default EDID path

2. **Added 720p60 EDID** (`src/aiden_sdk.cpp`)
   - Added `kDefaultHdmiEdid720p60[]` array from `edid/hdmi_720p60_cta.hex`
   - Kept existing `kDefaultHdmiEdid1080p30[]` for backward compatibility

3. **Smart EDID Selection** (`src/aiden_sdk.cpp`)
   - `push_edid()` now selects EDID based on requested resolution:
     - `height <= 720` → uses 720p60 EDID
     - `height > 720` → uses 1080p30 EDID
   - Custom EDID path via `--edid` flag still takes precedence

4. **Updated Tests** (`tests/frame_service_defaults_test.cpp`)
   - Updated default resolution expectations to 1280x720

## Usage

### Default Behavior (720p)
```bash
# Now works on Pixel 8 and other 720p-only devices
frame_service
```

### Override to 1080p
```bash
# For devices that support 1080p
frame_service --width 1920 --height 1080

# Or via environment variables
export AIDEN_DEFAULT_WIDTH=1920
export AIDEN_DEFAULT_HEIGHT=1080
frame_service
```

### Custom EDID
```bash
# Use a specific EDID file
frame_service --edid /usr/share/aiden/edid/hdmi_1080p60_cta.hex
```

## Benefits

1. **Works out-of-box on more devices**: Pixel 8, and other phones with 720p HDMI output limits
2. **Backward compatible**: 1080p devices can still use higher resolution via `--width`/`--height` flags
3. **Flexible**: Environment variables allow system-wide defaults without code changes
4. **Automatic EDID matching**: No need to manually specify EDID for common resolutions

## Technical Details

- The 720p60 EDID (`edid/hdmi_720p60_cta.hex`) advertises support for 1280x720@60Hz
- The coordinate system in `touch_gesture` already scaled dynamically, so no changes needed there
- Frame format conversion (`frame_convert.go`) was already resolution-agnostic
- The only blocker was the hardcoded 1080p EDID and default resolution

## Testing

To verify on Pixel 8:
```bash
# Should now succeed without SERVICE_RECOVERING errors
aiden-agent --device pixel8 touch_gesture '{"action": "tap", "x": 500, "y": 500}'
```

## Related Files

- `src/camera_frame_utils.h` - Default config with env var support
- `src/aiden_sdk.cpp` - EDID arrays and selection logic
- `src/frame_service_main.cpp` - Command-line option parsing
- `tests/frame_service_defaults_test.cpp` - Updated expectations
