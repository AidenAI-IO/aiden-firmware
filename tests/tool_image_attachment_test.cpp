#include "doctest.h"
#include "tool_image_attachment.h"
#include <cstdio>
#include <string>
#include <unistd.h>

using aiden::ImageAttachment;
using aiden::build_image_attachment_from_tool_result;

TEST_CASE("build_image_attachment_from_tool_result converts screenshot PNG to data URL") {
    char path[] = "/tmp/aiden-tool-image-test-XXXXXX";
    int fd = mkstemp(path);
    REQUIRE(fd >= 0);
    FILE* fp = fdopen(fd, "wb");
    REQUIRE(fp != nullptr);
    const unsigned char bytes[] = {0x89, 0x50, 0x4e, 0x47};
    REQUIRE(std::fwrite(bytes, 1, sizeof(bytes), fp) == sizeof(bytes));
    std::fclose(fp);

    std::string result = std::string("{\"ok\":true,\"path\":\"") + path +
                         "\",\"seq\":\"5\",\"format\":\"png\",\"width\":2,\"height\":1}";
    ImageAttachment attachment;
    CHECK(build_image_attachment_from_tool_result("capture_screenshot", result.c_str(), &attachment));
    CHECK(attachment.data_url == "data:image/png;base64,iVBORw==");
    CHECK(attachment.text == "Screenshot captured from frame_service (seq=5, 2x1).");
    std::remove(path);
}

TEST_CASE("build_image_attachment_from_tool_result ignores non-screenshot tool results") {
    ImageAttachment attachment;
    CHECK_FALSE(build_image_attachment_from_tool_result("frame_service_health", R"({"ok":true})", &attachment));
}
