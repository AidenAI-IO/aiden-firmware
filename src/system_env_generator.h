#pragma once

#include <string>

namespace aiden {

struct GeneratedSystemEnv {
    bool valid;
    std::string content;
    std::string error;
};

bool generate_systemd_environment(const std::string& input,
                                  GeneratedSystemEnv* generated);

}  // namespace aiden
