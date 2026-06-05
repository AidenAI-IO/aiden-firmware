#include "doctest.h"
#include "camera_frame_utils.h"
#include "frame_service_defaults.h"

TEST_CASE("frame_service defaults use 720p60 EDID for better device compatibility") {
    aiden::CameraConfig camera;
    aiden_demo::set_default_camera_config(&camera);

    CHECK(camera.width == 1280);
    CHECK(camera.height == 720);
    CHECK(camera.edid_path == nullptr);
    CHECK(aiden::kDefaultFrameServiceRingSize == 3);
    CHECK(aiden::kDefaultFrameServiceFps == 3.0);
    CHECK(aiden::kDefaultScreenshotMaxEdge == 960);
}
