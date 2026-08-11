#include "doctest.h"
#include "camera_frame_utils.h"
#include "frame_service_defaults.h"

TEST_CASE("frame_service defaults force a 1080p60 EDID with modest sampling fps") {
    aiden::CameraConfig camera;
    aiden_demo::set_default_camera_config(&camera);

    CHECK(camera.width == 1920);
    CHECK(camera.height == 1080);
    CHECK(camera.edid_path == nullptr);
    CHECK(camera.force_trigger == true);
    CHECK(camera.require_exact_resolution == true);
    CHECK(aiden::kDefaultFrameServiceRingSize == 3);
    CHECK(aiden::kDefaultFrameServiceFps == 3.0);
    CHECK(aiden::kDefaultScreenshotMaxEdge == 960);
}
