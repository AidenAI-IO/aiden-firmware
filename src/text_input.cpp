#include "text_input.h"

#include <cstddef>
#include <cstdint>

namespace aiden {
namespace {

bool is_utf8_continuation(unsigned char byte) {
    return (byte & 0xc0) == 0x80;
}

void erase_previous_utf8_codepoint(std::string& line) {
    if (line.empty()) return;

    size_t pos = line.size() - 1;
    while (pos > 0 && is_utf8_continuation(static_cast<unsigned char>(line[pos]))) {
        pos--;
    }
    line.erase(pos);
}

bool is_text_byte(unsigned char byte) {
    return byte >= 0x20;
}

bool consume_escape_byte(TextInputState& state, unsigned char byte) {
    const int kEscapeNone = 0;
    const int kEscapeStarted = 1;
    const int kEscapeSequence = 2;

    if (state.escape_state == kEscapeStarted) {
        state.escape_state = (byte == '[' || byte == 'O') ? kEscapeSequence : kEscapeNone;
        return true;
    }

    if (state.escape_state == kEscapeSequence) {
        if (byte >= 0x40 && byte <= 0x7e) state.escape_state = kEscapeNone;
        return true;
    }

    if (byte == 0x1b) {
        state.escape_state = kEscapeStarted;
        return true;
    }

    return false;
}

uint32_t decode_utf8_codepoint(const std::string& text, size_t& index) {
    unsigned char first = static_cast<unsigned char>(text[index++]);
    if (first < 0x80) return first;

    uint32_t codepoint = 0;
    size_t extra = 0;
    if ((first & 0xe0) == 0xc0) {
        codepoint = first & 0x1f;
        extra = 1;
    } else if ((first & 0xf0) == 0xe0) {
        codepoint = first & 0x0f;
        extra = 2;
    } else if ((first & 0xf8) == 0xf0) {
        codepoint = first & 0x07;
        extra = 3;
    } else {
        return first;
    }

    for (size_t i = 0; i < extra; i++) {
        if (index >= text.size()) return first;

        unsigned char byte = static_cast<unsigned char>(text[index]);
        if (!is_utf8_continuation(byte)) return first;

        codepoint = (codepoint << 6) | (byte & 0x3f);
        index++;
    }

    return codepoint;
}

// This is a small terminal-width approximation for CJK/emoji input, not a
// complete Unicode wcwidth implementation. Use generated Unicode tables if
// broader locale support becomes necessary.
bool is_combining_codepoint(uint32_t codepoint) {
    return (codepoint >= 0x0300 && codepoint <= 0x036f) ||
           (codepoint >= 0x1ab0 && codepoint <= 0x1aff) ||
           (codepoint >= 0x1dc0 && codepoint <= 0x1dff) ||
           (codepoint >= 0x20d0 && codepoint <= 0x20ff) ||
           (codepoint >= 0xfe20 && codepoint <= 0xfe2f);
}

bool is_wide_codepoint(uint32_t codepoint) {
    return codepoint >= 0x1100 &&
           (codepoint <= 0x115f ||
            codepoint == 0x2329 || codepoint == 0x232a ||
            (codepoint >= 0x2e80 && codepoint <= 0xa4cf && codepoint != 0x303f) ||
            (codepoint >= 0xac00 && codepoint <= 0xd7a3) ||
            (codepoint >= 0xf900 && codepoint <= 0xfaff) ||
            (codepoint >= 0xfe10 && codepoint <= 0xfe19) ||
            (codepoint >= 0xfe30 && codepoint <= 0xfe6f) ||
            (codepoint >= 0xff00 && codepoint <= 0xff60) ||
            (codepoint >= 0xffe0 && codepoint <= 0xffe6) ||
            (codepoint >= 0x1f300 && codepoint <= 0x1f64f) ||
            (codepoint >= 0x1f900 && codepoint <= 0x1f9ff) ||
            (codepoint >= 0x20000 && codepoint <= 0x3fffd));
}

int codepoint_display_width(uint32_t codepoint) {
    if (codepoint == 0) return 0;
    if (codepoint < 0x20 || (codepoint >= 0x7f && codepoint < 0xa0)) return 0;
    if (is_combining_codepoint(codepoint)) return 0;
    return is_wide_codepoint(codepoint) ? 2 : 1;
}

}

TextInputLineStatus apply_text_input_byte(TextInputState& state,
                                          std::string& line,
                                          unsigned char byte) {
    if (consume_escape_byte(state, byte)) return TextInputLineStatus::InProgress;

    if (byte == '\n' || byte == '\r') return TextInputLineStatus::Complete;
    if (byte == 0x03) return TextInputLineStatus::Interrupt;
    if (byte == 0x04) return line.empty() ? TextInputLineStatus::Eof
                                          : TextInputLineStatus::InProgress;

    if (byte == 0x7f || byte == 0x08) {
        erase_previous_utf8_codepoint(line);
        return TextInputLineStatus::InProgress;
    }

    if (is_text_byte(byte)) line.push_back(static_cast<char>(byte));
    return TextInputLineStatus::InProgress;
}

int text_display_width(const std::string& text) {
    int width = 0;
    size_t index = 0;
    while (index < text.size()) {
        width += codepoint_display_width(decode_utf8_codepoint(text, index));
    }
    return width;
}

}
