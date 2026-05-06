#pragma once
#include <string>

namespace aiden {

std::string build_chat_completions_url(const char* configured_base_url,
                                       const char* default_base_url);

}
