#pragma once

#include <string>
#include <vector>

namespace aiden {

struct EnvAssignment {
    std::string key;
    std::string value;
};

struct SystemEnvProxy {
    std::string http_proxy;
    std::string https_proxy;
    std::string all_proxy;
    std::string no_proxy;
};

std::string validate_system_proxy_url(const std::string& url);

bool parse_system_env_content(const std::string& content,
                              std::vector<EnvAssignment>* assignments,
                              SystemEnvProxy* proxy,
                              std::string* error);

}  // namespace aiden
