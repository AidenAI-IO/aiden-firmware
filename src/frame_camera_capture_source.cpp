#include "frame_camera_capture_source.h"
#include "aiden_log.h"
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <limits>
#include <stdio.h>
#include <string.h>
#include <sys/file.h>
#include <unistd.h>

namespace aiden {

FrameCameraCaptureSource::FrameCameraCaptureSource(const CameraConfig& config)
    : config_(config),
      device_name_(config.device_name ? config.device_name : "/dev/video0"),
      pixel_format_(config.pixel_format ? config.pixel_format : "nv12"),
      subdev_device_(config.subdev_device && strcmp(config.subdev_device, "auto") != 0
                         ? config.subdev_device : ""),
      edid_path_(config.edid_path ? config.edid_path : ""),
      auto_subdev_(!config.subdev_device || strcmp(config.subdev_device, "auto") == 0),
      auto_edid_(!config.edid_path),
      auto_force_trigger_pending_(auto_subdev_),
      // Automatic discovery may need an occasional bounded HPD/EDID retry
      // while a hot-plugged source is settling.  An explicit --force-trigger
      // keeps its historical one-shot behaviour instead of silently turning
      // into a periodic EDID writer.
      periodic_force_enabled_(auto_subdev_),
      tc_open_attempts_(0),
      force_trigger_pending_(config.force_trigger),
      lock_fd_(-1) {
    sync_config_strings();
}

std::string FrameCameraCaptureSource::detect_hdmi_subdev(std::string* name) {
    if (name) {
        name->clear();
    }
    DIR* directory = opendir("/sys/class/video4linux");
    if (!directory) {
        return std::string();
    }

    std::string result;
    struct dirent* entry = nullptr;
    while ((entry = readdir(directory)) != nullptr) {
        if (strncmp(entry->d_name, "v4l-subdev", 10) != 0) {
            continue;
        }
        char name_path[256];
        snprintf(name_path, sizeof(name_path), "/sys/class/video4linux/%s/name", entry->d_name);
        FILE* file = fopen(name_path, "r");
        if (!file) {
            continue;
        }
        char buffer[256] = {};
        if (!fgets(buffer, sizeof(buffer), file)) {
            fclose(file);
            continue;
        }
        fclose(file);
        buffer[strcspn(buffer, "\r\n")] = '\0';
        if (!strstr(buffer, "rk628-csi") && !strstr(buffer, "tc358743")) {
            continue;
        }
        result = std::string("/dev/") + entry->d_name;
        if (name) {
            *name = buffer;
        }
        break;
    }
    closedir(directory);
    return result;
}

void FrameCameraCaptureSource::sync_config_strings() {
    config_.device_name = device_name_.c_str();
    config_.pixel_format = pixel_format_.c_str();
    config_.subdev_device = auto_subdev_ && subdev_device_.empty()
        ? nullptr : subdev_device_.c_str();
    config_.edid_path = edid_path_.empty() ? nullptr : edid_path_.c_str();
}

bool FrameCameraCaptureSource::open() {
    std::string bridge_name;
    config_.allow_edid_fallback = !periodic_force_enabled_;
    if (auto_subdev_) {
        subdev_device_ = detect_hdmi_subdev(&bridge_name);
        if (subdev_device_.empty()) {
            AIDEN_LOG_WARN("hdmi", "bridge_not_ready",
                           "frame capture will retry until an HDMI bridge appears");
            return false;
        }

        // Keep the bridge-aware policy when the kernel registers the subdevice
        // after frame_service has already started.  RK628D uses its driver
        // EDID, while TC358743 needs the HPD/EDID renegotiation path.
    } else {
        char name_path[256];
        snprintf(name_path, sizeof(name_path), "/sys/class/video4linux/%s/name",
                 strrchr(subdev_device_.c_str(), '/')
                     ? strrchr(subdev_device_.c_str(), '/') + 1 : subdev_device_.c_str());
        FILE* file = fopen(name_path, "r");
        if (file) {
            char buffer[256] = {};
            if (fgets(buffer, sizeof(buffer), file)) {
                buffer[strcspn(buffer, "\r\n")] = '\0';
                bridge_name = buffer;
            }
            fclose(file);
        }
    }

    if (bridge_name.find("tc358743") != std::string::npos) {
        ++tc_open_attempts_;
        // Try HPD/EDID immediately and then at a bounded cadence.  This
        // handles a TC358743 that becomes usable only after the source is
        // plugged in, without writing the bridge on every recovery loop.
        const bool first_force = auto_force_trigger_pending_;
        auto_force_trigger_pending_ = false;
        const bool periodic_force = periodic_force_enabled_ &&
            (tc_open_attempts_ % 6U) == 1U;
        config_.force_trigger = force_trigger_pending_ || first_force || periodic_force;
        // A failed TC358743 timing probe must not silently fall through to an
        // EDID write on every recovery loop.  Only the bounded force attempt
        // below is allowed to perform that renegotiation.
        config_.allow_edid_fallback = false;
        if (edid_path_.empty()) {
            const char* default_edid = "/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex";
            if (access(default_edid, R_OK) == 0) {
                edid_path_ = default_edid;
            }
        }
    } else if (auto_subdev_) {
        auto_force_trigger_pending_ = false;
        config_.force_trigger = force_trigger_pending_;
        config_.allow_edid_fallback = false;
        if (auto_edid_) {
            edid_path_.clear();
        }
    } else {
        config_.force_trigger = force_trigger_pending_;
        config_.allow_edid_fallback = true;
    }
    if (config_.force_trigger) {
        config_.allow_edid_fallback = true;
    }
    sync_config_strings();

    const bool force_attempt = config_.force_trigger;

    // Keep device ownership inside the recoverable capture source.  If
    // /dev/video0 is temporarily absent or held by a diagnostic process, the
    // IPC service remains alive and the manager retries instead of terminating
    // the entire systemd service.
    if (lock_fd_ < 0) {
        lock_fd_ = ::open(device_name_.c_str(), O_RDONLY | O_CLOEXEC);
        if (lock_fd_ < 0) {
            AIDEN_LOG_WARN("camera", "device_lock_open_pending",
                           "device=%s error=%s", device_name_.c_str(), strerror(errno));
            return false;
        }
        if (flock(lock_fd_, LOCK_EX | LOCK_NB) < 0) {
            AIDEN_LOG_WARN("camera", "device_lock_pending",
                           "device=%s error=%s", device_name_.c_str(), strerror(errno));
            ::close(lock_fd_);
            lock_fd_ = -1;
            return false;
        }
    }

    const bool opened = camera_.init(config_);
    if (force_attempt) {
        // A failed force-trigger is recoverable.  Do not repeat HPD/EDID
        // writes on every retry; auto-subdevice periodically schedules another
        // bounded attempt for TC358743 when needed.
        force_trigger_pending_ = false;
    }
    if (opened) {
        force_trigger_pending_ = false;
        if (auto_subdev_) {
            auto_force_trigger_pending_ = false;
        }
    } else {
        ::close(lock_fd_);
        lock_fd_ = -1;
    }
    return opened;
}

bool FrameCameraCaptureSource::pause() {
    return camera_.pause();
}

bool FrameCameraCaptureSource::resume() {
    return camera_.resume();
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
    if (pixel_format_ == "nv12") {
        frame->metadata.stride = video_frame.width;
    } else if (video_frame.stride > 0) {
        frame->metadata.stride = video_frame.stride;
    } else if (pixel_format_ == "uyvy" || pixel_format_ == "yuyv" || pixel_format_ == "nv16") {
        frame->metadata.stride = video_frame.width * 2U;
    } else {
        frame->metadata.stride = video_frame.width;
    }
    frame->metadata.planes.clear();
    if (pixel_format_ == "nv12" || pixel_format_ == "nv16") {
        const uint64_t y_bytes_u64 = static_cast<uint64_t>(video_frame.width) *
            video_frame.height;
        const uint64_t uv_bytes_u64 = pixel_format_ == "nv12"
            ? y_bytes_u64 / 2U : y_bytes_u64;
        if (y_bytes_u64 > std::numeric_limits<uint32_t>::max() ||
            uv_bytes_u64 > std::numeric_limits<uint32_t>::max() ||
            y_bytes_u64 + uv_bytes_u64 > buffer.size()) {
            AIDEN_LOG_ERROR("camera", "frame_layout_incomplete",
                            "format=%s width=%u height=%u bytes=%zu expected=%llu",
                            pixel_format_.c_str(), video_frame.width, video_frame.height,
                            buffer.size(),
                            static_cast<unsigned long long>(y_bytes_u64 + uv_bytes_u64));
            return false;
        }
        const uint32_t y_bytes = static_cast<uint32_t>(y_bytes_u64);
        const uint32_t uv_bytes = static_cast<uint32_t>(uv_bytes_u64);
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

bool FrameCameraCaptureSource::discard() {
    return camera_.discard_frame_timeout(500);
}

void FrameCameraCaptureSource::close() {
    camera_.stop();
    if (lock_fd_ >= 0) {
        ::close(lock_fd_);
        lock_fd_ = -1;
    }
}

}  // namespace aiden
