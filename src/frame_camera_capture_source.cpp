#include "frame_camera_capture_source.h"

namespace aiden {

FrameCameraCaptureSource::FrameCameraCaptureSource(const CameraConfig& config)
    : config_(config),
      device_name_(config.device_name ? config.device_name : "/dev/video0"),
      pixel_format_(config.pixel_format ? config.pixel_format : "uyvy"),
      subdev_device_(config.subdev_device ? config.subdev_device : "/dev/v4l-subdev2"),
      edid_path_(config.edid_path ? config.edid_path : "") {
    sync_config_strings();
}

void FrameCameraCaptureSource::sync_config_strings() {
    config_.device_name = device_name_.c_str();
    config_.pixel_format = pixel_format_.c_str();
    config_.subdev_device = subdev_device_.c_str();
    config_.edid_path = edid_path_.empty() ? nullptr : edid_path_.c_str();
}

bool FrameCameraCaptureSource::open() {
    sync_config_strings();
    return camera_.init(config_);
}

bool FrameCameraCaptureSource::capture(CapturedFrame* frame) {
    if (!frame) {
        return false;
    }

    VideoFrame video_frame{};
    std::vector<uint8_t> buffer;
    if (!camera_.capture_frame_timeout(video_frame, buffer, 500)) {
        return false;
    }

    frame->metadata.capture_ts_ns = video_frame.timestamp * 1000ULL;
    frame->metadata.width = video_frame.width;
    frame->metadata.height = video_frame.height;
    frame->metadata.pixel_format = pixel_format_;
    frame->metadata.bytes = buffer.size();
    if (pixel_format_ == "uyvy" || pixel_format_ == "yuyv" || pixel_format_ == "nv16") {
        frame->metadata.stride = video_frame.width * 2;
    } else {
        frame->metadata.stride = video_frame.width;
    }
    frame->metadata.planes.clear();
    if (pixel_format_ == "nv12" || pixel_format_ == "nv16") {
        const uint32_t y_bytes = video_frame.width * video_frame.height;
        const uint32_t uv_bytes = pixel_format_ == "nv12" ? y_bytes / 2 : y_bytes;
        FramePlaneMetadata y_plane;
        y_plane.offset = 0;
        y_plane.stride = video_frame.width;
        y_plane.bytes = y_bytes;
        frame->metadata.planes.push_back(y_plane);
        FramePlaneMetadata uv_plane;
        uv_plane.offset = y_bytes;
        uv_plane.stride = video_frame.width;
        uv_plane.bytes = uv_bytes;
        frame->metadata.planes.push_back(uv_plane);
    }
    frame->data.swap(buffer);
    return true;
}

void FrameCameraCaptureSource::close() {
    camera_.stop();
}

}  // namespace aiden
