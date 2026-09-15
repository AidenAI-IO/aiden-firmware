#include "doctest.h"
#include "camera_frame_utils.h"
#include "frame_service_defaults.h"
#include <string>

TEST_CASE("frame_service library defaults avoid unconditional EDID retriggering") {
    aiden::CameraConfig camera;
    aiden_demo::set_default_camera_config(&camera);

    CHECK(camera.width == 1920);
    CHECK(camera.height == 1080);
    CHECK(std::string(camera.pixel_format) == "uyvy");
    CHECK(camera.edid_path == nullptr);
    CHECK(camera.force_trigger == false);
    CHECK(camera.allow_edid_fallback == true);
    CHECK(camera.require_exact_resolution == true);
    CHECK(aiden::kDefaultFrameServiceRingSize == 3);
    CHECK(aiden::kDefaultFrameServiceFps == 3.0);
    CHECK(aiden::kDefaultScreenshotMaxEdge == 960);
    CHECK(aiden::default_frame_service_warmup_frames(false) == 0);
    CHECK(aiden::default_frame_service_warmup_frames(true) == 6);
}
