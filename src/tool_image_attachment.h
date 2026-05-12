#pragma once

#include <string>

namespace aiden {

struct ImageAttachment {
    std::string data_url;
    std::string text;
};

bool build_image_attachment_from_tool_result(const char* tool_name,
                                             const char* result_json,
                                             ImageAttachment* out);

}
