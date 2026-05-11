#include "doctest.h"
#include "text_input.h"

#include <string>

TEST_CASE("text input backspace erases one UTF-8 codepoint") {
    aiden::TextInputState state;
    std::string line = "你好A";

    CHECK(aiden::apply_text_input_byte(state, line, 0x7f) == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "你好");

    CHECK(aiden::apply_text_input_byte(state, line, 0x7f) == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "你");

    CHECK(aiden::apply_text_input_byte(state, line, 0x08) == aiden::TextInputLineStatus::InProgress);
    CHECK(line.empty());
}

TEST_CASE("text input accepts UTF-8 bytes until newline completes the line") {
    aiden::TextInputState state;
    std::string line;

    const std::string text = "中文";
    for (unsigned char byte : text) {
        CHECK(aiden::apply_text_input_byte(state, line, byte) == aiden::TextInputLineStatus::InProgress);
    }

    CHECK(line == text);
    CHECK(aiden::apply_text_input_byte(state, line, '\n') == aiden::TextInputLineStatus::Complete);
}

TEST_CASE("text input ctrl-d only exits on an empty line") {
    aiden::TextInputState state;
    std::string line;
    CHECK(aiden::apply_text_input_byte(state, line, 0x04) == aiden::TextInputLineStatus::Eof);

    line = "keep";
    CHECK(aiden::apply_text_input_byte(state, line, 0x04) == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "keep");
}

TEST_CASE("text input ctrl-c interrupts immediately") {
    aiden::TextInputState state;
    std::string line = "draft";

    CHECK(aiden::apply_text_input_byte(state, line, 0x03) == aiden::TextInputLineStatus::Interrupt);
    CHECK(line == "draft");
}

TEST_CASE("text input ignores tab to avoid terminal column drift") {
    aiden::TextInputState state;
    std::string line = "before";

    CHECK(aiden::apply_text_input_byte(state, line, '\t') == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "before");
}

TEST_CASE("text input discards CSI and SS3 escape sequences") {
    aiden::TextInputState state;
    std::string line = "cmd";

    CHECK(aiden::apply_text_input_byte(state, line, 0x1b) == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, '[') == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, 'A') == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "cmd");

    CHECK(aiden::apply_text_input_byte(state, line, 0x1b) == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, '[') == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, '3') == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, '~') == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "cmd");

    CHECK(aiden::apply_text_input_byte(state, line, 0x1b) == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, 'O') == aiden::TextInputLineStatus::InProgress);
    CHECK(aiden::apply_text_input_byte(state, line, 'P') == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "cmd");

    CHECK(aiden::apply_text_input_byte(state, line, 'X') == aiden::TextInputLineStatus::InProgress);
    CHECK(line == "cmdX");
}

TEST_CASE("text display width treats CJK as two terminal columns") {
    CHECK(aiden::text_display_width("A") == 1);
    CHECK(aiden::text_display_width("你") == 2);
    CHECK(aiden::text_display_width("你好A") == 5);
}
