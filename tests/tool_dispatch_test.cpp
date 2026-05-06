#include "doctest.h"
#include "tool_dispatch.h"
#include <string>

using aiden::ToolCommandResult;
using aiden::build_tool_command;

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
