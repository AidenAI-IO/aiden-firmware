#include "llm_api_url.h"

namespace aiden {

static std::string strip_trailing_slashes(const std::string& url) {
    std::string normalized = url;
    while (!normalized.empty() && normalized[normalized.size() - 1] == '/') {
        normalized.erase(normalized.size() - 1);
    }
    return normalized;
}

std::string build_chat_completions_url(const char* configured_base_url,
                                       const char* default_base_url) {
    const char* raw_base = configured_base_url && configured_base_url[0] != '\0'
        ? configured_base_url
        : default_base_url;
    return strip_trailing_slashes(raw_base ? raw_base : "") + "/chat/completions";
}

}
