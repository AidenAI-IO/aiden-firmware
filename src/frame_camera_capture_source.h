#pragma once

#include "aiden_sdk.h"
#include "frame_capture_manager.h"
#include <string>

namespace aiden {

class FrameCameraCaptureSource : public FrameCaptureSource {
public:
    explicit FrameCameraCaptureSource(const CameraConfig& config);

    bool open() override;
    bool capture(CapturedFrame* frame) override;
    bool discard() override;
    void close() override;

private:
    void sync_config_strings();

    CameraConfig config_;
    std::string device_name_;
    std::string pixel_format_;
    std::string subdev_device_;
    std::string edid_path_;
    bool force_trigger_pending_;
    CameraCapture camera_;
};

}  // namespace aiden
