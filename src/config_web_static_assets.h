#ifndef AIDEN_CONFIG_WEB_STATIC_ASSETS_H
#define AIDEN_CONFIG_WEB_STATIC_ASSETS_H

#include <string>
#include <utility>
#include <vector>

namespace aiden {

struct ConfigWebStaticAssetResponse {
    bool handled = false;
    int status_code = 200;
    std::string status_text = "OK";
    std::string content_type = "application/octet-stream";
    std::string body;
    int body_fd = -1;
    unsigned long long body_fd_size = 0;
    std::vector<std::pair<std::string, std::string> > headers;
};

ConfigWebStaticAssetResponse serve_config_web_static_asset(
    const std::string& web_root,
    const std::string& method,
    const std::string& request_path);

}  // namespace aiden

#endif  // AIDEN_CONFIG_WEB_STATIC_ASSETS_H
