#include "minimax_codec.h"
#include "cJSON/cJSON.h"
#include <string.h>

namespace aiden {
namespace minimax {

static uint8_t hex_to_byte(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return 0;
}

std::vector<uint8_t> hex_decode(const char* hex) {
    std::vector<uint8_t> result;
    size_t len = strlen(hex);
    for (size_t i = 0; i + 1 < len; i += 2) {
        uint8_t byte = (hex_to_byte(hex[i]) << 4) | hex_to_byte(hex[i + 1]);
        result.push_back(byte);
    }
    return result;
}

std::vector<std::vector<uint8_t>> StreamParser::feed(const char* data, size_t len) {
    std::vector<std::vector<uint8_t>> out;
    buffer_.append(data, len);

    size_t pos = 0;
    while (pos < buffer_.size()) {
        size_t start = buffer_.find('{', pos);
        if (start == std::string::npos) {
            buffer_.clear();
            break;
        }
        if (start > 0) {
            buffer_.erase(0, start);
            start = 0;
        }

        int depth = 0;
        size_t end = start;
        bool in_string = false;
        bool escaped = false;

        for (size_t i = start; i < buffer_.size(); i++) {
            char c = buffer_[i];

            if (escaped) {
                escaped = false;
                continue;
            }
            if (c == '\\') {
                escaped = true;
                continue;
            }
            if (c == '"') {
                in_string = !in_string;
                continue;
            }
            if (in_string) continue;

            if (c == '{') depth++;
            else if (c == '}') {
                depth--;
                if (depth == 0) {
                    end = i + 1;
                    break;
                }
            }
        }

        if (depth != 0) break;

        std::string json_str = buffer_.substr(start, end - start);
        buffer_.erase(0, end);
        pos = 0;

        cJSON* json = cJSON_Parse(json_str.c_str());
        if (!json) continue;

        cJSON* data_obj = cJSON_GetObjectItem(json, "data");
        if (data_obj) {
            cJSON* audio = cJSON_GetObjectItem(data_obj, "audio");
            if (audio && audio->type == cJSON_String) {
                std::vector<uint8_t> mp3_chunk = hex_decode(audio->valuestring);
                if (!mp3_chunk.empty()) out.push_back(mp3_chunk);
            }
        }

        cJSON_Delete(json);
    }

    return out;
}

void StreamParser::reset() {
    buffer_.clear();
}

}
}
