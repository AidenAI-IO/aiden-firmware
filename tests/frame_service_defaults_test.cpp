#include "doctest.h"
#include "frame_service_defaults.h"

TEST_CASE("frame_service defaults keep memory footprint modest") {
    CHECK(aiden::kDefaultFrameServiceRingSize == 3);
    CHECK(aiden::kDefaultFrameServiceFps == 3.0);
    CHECK(aiden::kDefaultScreenshotMaxEdge == 960);
}
