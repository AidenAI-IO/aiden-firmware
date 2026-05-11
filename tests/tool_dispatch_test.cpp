#include "doctest.h"
#include "cJSON/cJSON.h"
#include "frame_service_server.h"
#include "tool_dispatch.h"
#include <cstring>
#include <cstdio>
#include <string>
#include <unistd.h>
#include <vector>

using aiden::ToolCommandResult;
using aiden::build_tool_command;
using aiden::FrameMetadata;
using aiden::FrameServiceServer;
using aiden::FrameServiceStatus;
using aiden::handle_frame_tool;

namespace {

struct TempSocketPath {
    std::string path;
    TempSocketPath() {
        char tmpl[] = "/tmp/aiden_tool_socket_XXXXXX";
        int fd = ::mkstemp(tmpl);
        REQUIRE(fd >= 0);
        ::close(fd);
        ::unlink(tmpl);
        path = tmpl;
    }
    ~TempSocketPath() { ::unlink(path.c_str()); }
};

FrameMetadata tool_frame_metadata(uint32_t width = 2, uint32_t height = 1) {
    FrameMetadata metadata;
    metadata.width = width;
    metadata.height = height;
    metadata.pixel_format = "uyvy";
    metadata.stride = width * 2;
    metadata.bytes = width * height * 2;
    metadata.capture_ts_ns = 10;
    return metadata;
}

}

TEST_CASE("keyboard_tap builds sequential key tap command") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_tap", R"({"keys":["CTRL","C"]})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid keyboard tap 'CTRL' 'C'");
}

TEST_CASE("keyboard_tap shell-escapes key tokens safely") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_tap", R"({"keys":["A; rm -rf /","it's"]})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid keyboard tap 'A; rm -rf /' 'it'\\''s'");
}

TEST_CASE("keyboard_tap requires keys array") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_tap", R"({"keys":"CTRL"})");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: missing keys array");
}

TEST_CASE("keyboard_text shell-escapes single quotes safely") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_text", R"({"text":"it's done"})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid keyboard text 'it'\\''s done'");
}

TEST_CASE("keyboard_text preserves shell metacharacters as literal text") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_text", R"({"text":"$(rm -rf /); echo hi"})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid keyboard text '$(rm -rf /); echo hi'");
}

TEST_CASE("keyboard_text requires text") {
    ToolCommandResult r = build_tool_command("./hid", "keyboard_text", R"({})");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: missing text");
}

TEST_CASE("touch_click builds coordinate command") {
    ToolCommandResult r = build_tool_command("./hid", "touch_click", R"({"x":123,"y":456})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid touch click 123 456");
}

TEST_CASE("touch_click requires both coordinates") {
    ToolCommandResult r = build_tool_command("./hid", "touch_click", R"({"x":123})");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: missing x or y");
}

TEST_CASE("touch_swipe builds down move up chain") {
    ToolCommandResult r = build_tool_command("./hid", "touch_swipe", R"({"x1":1,"y1":2,"x2":3,"y2":4})");
    REQUIRE(r.ok);
    CHECK(r.command == "sudo ./hid touch down 1 2 && sudo ./hid touch move 3 4 && sudo ./hid touch up");
}

TEST_CASE("touch_swipe requires all coordinates") {
    ToolCommandResult r = build_tool_command("./hid", "touch_swipe", R"({"x1":1,"y1":2,"x2":3})");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: missing coordinates");
}

TEST_CASE("invalid JSON is rejected") {
    ToolCommandResult r = build_tool_command("./hid", "touch_click", "{broken");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: invalid JSON");
}

TEST_CASE("unknown tools are rejected") {
    ToolCommandResult r = build_tool_command("./hid", "boom", R"({})");
    CHECK_FALSE(r.ok);
    CHECK(r.error == "error: unknown tool");
}

TEST_CASE("capture_screenshot frame tool writes PNG by default and returns metadata JSON") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    uint64_t seq = 0;
    REQUIRE(server.append_frame(tool_frame_metadata(), uyvy.data(), uyvy.size(), &seq) == FrameServiceStatus::OK);

    std::string result = handle_frame_tool(socket_path.path.c_str(), "capture_screenshot", R"({})", "/tmp");
    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_True);
    CHECK(std::string(cJSON_GetObjectItem(json, "format")->valuestring) == "png");
    CHECK(std::string(cJSON_GetObjectItem(json, "seq")->valuestring) == "1");
    CHECK(cJSON_GetObjectItem(json, "width")->valueint == 2);
    CHECK(cJSON_GetObjectItem(json, "height")->valueint == 1);
    std::string path = cJSON_GetObjectItem(json, "path")->valuestring;
    cJSON_Delete(json);

    FILE* fp = std::fopen(path.c_str(), "rb");
    REQUIRE(fp != nullptr);
    unsigned char magic[8];
    REQUIRE(std::fread(magic, 1, 8, fp) == 8);
    const unsigned char png_magic[] = {137, 80, 78, 71, 13, 10, 26, 10};
    CHECK(std::memcmp(magic, png_magic, sizeof(png_magic)) == 0);
    std::fclose(fp);
    std::remove(path.c_str());
    server.stop();
}

TEST_CASE("capture_screenshot frame tool writes BMP when requested") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    uint64_t seq = 0;
    REQUIRE(server.append_frame(tool_frame_metadata(), uyvy.data(), uyvy.size(), &seq) == FrameServiceStatus::OK);

    std::string result = handle_frame_tool(socket_path.path.c_str(), "capture_screenshot", R"({"format":"bmp"})", "/tmp");
    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_True);
    CHECK(std::string(cJSON_GetObjectItem(json, "format")->valuestring) == "bmp");
    std::string path = cJSON_GetObjectItem(json, "path")->valuestring;
    cJSON_Delete(json);

    FILE* fp = std::fopen(path.c_str(), "rb");
    REQUIRE(fp != nullptr);
    char magic[2];
    REQUIRE(std::fread(magic, 1, 2, fp) == 2);
    CHECK(magic[0] == 'B');
    CHECK(magic[1] == 'M');
    std::fclose(fp);
    std::remove(path.c_str());
    server.stop();
}

TEST_CASE("capture_screenshot frame tool scales PNG by max_edge") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> uyvy(4 * 2 * 2, 128);
    uint64_t seq = 0;
    REQUIRE(server.append_frame(tool_frame_metadata(4, 2), uyvy.data(), uyvy.size(), &seq) == FrameServiceStatus::OK);

    std::string result = handle_frame_tool(socket_path.path.c_str(), "capture_screenshot", R"({"max_edge":2})", "/tmp");
    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_True);
    CHECK(std::string(cJSON_GetObjectItem(json, "format")->valuestring) == "png");
    CHECK(cJSON_GetObjectItem(json, "width")->valueint == 2);
    CHECK(cJSON_GetObjectItem(json, "height")->valueint == 1);
    std::string path = cJSON_GetObjectItem(json, "path")->valuestring;
    cJSON_Delete(json);
    std::remove(path.c_str());
    server.stop();
}

TEST_CASE("frame_service_health frame tool returns health JSON") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    uint64_t seq = 0;
    REQUIRE(server.append_frame(tool_frame_metadata(), uyvy.data(), uyvy.size(), &seq) == FrameServiceStatus::OK);

    std::string result = handle_frame_tool(socket_path.path.c_str(), "frame_service_health", R"({})", "/tmp");
    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_True);
    CHECK(std::string(cJSON_GetObjectItem(json, "state")->valuestring) == "RUNNING");
    CHECK(cJSON_GetObjectItem(json, "latest_seq")->type == cJSON_String);
    CHECK(cJSON_GetObjectItem(json, "ring_buffer_size")->valueint == 2);
    cJSON_Delete(json);
    server.stop();
}

TEST_CASE("frame_service_restart frame tool invokes restart") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    bool restarted = false;
    server.set_restart_handler([&]() { restarted = true; });
    REQUIRE(server.start() == FrameServiceStatus::OK);

    std::string result = handle_frame_tool(socket_path.path.c_str(), "frame_service_restart", R"({})", "/tmp");
    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_True);
    CHECK(restarted);
    cJSON_Delete(json);
    server.stop();
}

TEST_CASE("capture_screenshot returns structured error when frame service is unavailable") {
    TempSocketPath socket_path;
    std::string result = handle_frame_tool(socket_path.path.c_str(), "capture_screenshot", R"({})", "/tmp");

    cJSON* json = cJSON_Parse(result.c_str());
    REQUIRE(json != nullptr);
    CHECK(cJSON_GetObjectItem(json, "ok")->type == cJSON_False);
    CHECK(std::string(cJSON_GetObjectItem(json, "status")->valuestring) == "TRANSPORT_ERROR");
    cJSON_Delete(json);
}
