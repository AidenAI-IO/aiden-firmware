#pragma once

#include <string>

namespace aiden {

enum class TextInputLineStatus {
    InProgress,
    Complete,
    Eof,
    Interrupt,
};

TextInputLineStatus apply_text_input_byte(std::string& line, unsigned char byte);
int text_display_width(const std::string& text);

}
