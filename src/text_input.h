#pragma once

#include <string>

namespace aiden {

enum class TextInputLineStatus {
    InProgress,
    Complete,
    Eof,
    Interrupt,
};

struct TextInputState {
    int escape_state;

    TextInputState() : escape_state(0) {}
};

TextInputLineStatus apply_text_input_byte(TextInputState& state,
                                          std::string& line,
                                          unsigned char byte);
int text_display_width(const std::string& text);

}
