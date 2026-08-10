#include "config_web_static_assets.h"

#include "aiden_log.h"

#include <errno.h>
#include <fcntl.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cctype>
#include <string>

namespace aiden {
namespace {

ConfigWebStaticAssetResponse make_error(int status_code,
                                        const char* status_text,
                                        const char* body) {
    ConfigWebStaticAssetResponse response;
    response.handled = true;
    response.status_code = status_code;
    response.status_text = status_text;
    response.content_type = "text/plain; charset=utf-8";
    response.body = body;
    response.headers.push_back(std::make_pair("Cache-Control", "no-cache"));
    return response;
}

int hex_value(char c) {
    if (c >= '0' && c <= '9') {
        return c - '0';
    }
    if (c >= 'a' && c <= 'f') {
        return c - 'a' + 10;
    }
    if (c >= 'A' && c <= 'F') {
        return c - 'A' + 10;
    }
    return -1;
}

bool decode_asset_segment(const std::string& encoded, std::string* decoded) {
    if (!decoded || encoded.empty() || encoded == "." || encoded == "..") {
        return false;
    }

    decoded->clear();
    decoded->reserve(encoded.size());
    for (size_t i = 0; i < encoded.size(); ++i) {
        unsigned char value = static_cast<unsigned char>(encoded[i]);
        if (encoded[i] == '%') {
            if (i + 2 >= encoded.size()) {
                return false;
            }
            const int high = hex_value(encoded[i + 1]);
            const int low = hex_value(encoded[i + 2]);
            if (high < 0 || low < 0) {
                return false;
            }
            value = static_cast<unsigned char>((high << 4) | low);
            i += 2;
        }

        if (value == 0 || value == '/' || value == '\\') {
            return false;
        }
        decoded->push_back(static_cast<char>(value));
    }

    return !decoded->empty() && *decoded != "." && *decoded != "..";
}

bool decode_asset_path(const std::string& request_path, std::string* relative_path) {
    static const std::string prefix = "/assets/";
    if (!relative_path || request_path.compare(0, prefix.size(), prefix) != 0) {
        return false;
    }

    const std::string encoded_path = request_path.substr(prefix.size());
    if (encoded_path.empty()) {
        return false;
    }

    relative_path->assign("assets");
    size_t offset = 0;
    while (offset <= encoded_path.size()) {
        const size_t slash = encoded_path.find('/', offset);
        const size_t end = slash == std::string::npos ? encoded_path.size() : slash;
        std::string decoded_segment;
        if (!decode_asset_segment(encoded_path.substr(offset, end - offset), &decoded_segment)) {
            return false;
        }
        relative_path->push_back('/');
        relative_path->append(decoded_segment);
        if (slash == std::string::npos) {
            break;
        }
        offset = slash + 1;
    }
    return true;
}

std::string lowercase_copy(const std::string& text) {
    std::string result = text;
    for (size_t i = 0; i < result.size(); ++i) {
        result[i] = static_cast<char>(std::tolower(static_cast<unsigned char>(result[i])));
    }
    return result;
}

std::string content_type_for_path(const std::string& path) {
    const size_t dot = path.rfind('.');
    const std::string extension = dot == std::string::npos
        ? std::string()
        : lowercase_copy(path.substr(dot));
    if (extension == ".html") return "text/html; charset=utf-8";
    if (extension == ".css") return "text/css; charset=utf-8";
    if (extension == ".js") return "application/javascript; charset=utf-8";
    if (extension == ".json") return "application/json; charset=utf-8";
    if (extension == ".svg") return "image/svg+xml";
    if (extension == ".png") return "image/png";
    if (extension == ".ico") return "image/x-icon";
    return "application/octet-stream";
}

ConfigWebStaticAssetResponse open_asset(const std::string& web_root,
                                        const std::string& relative_path,
                                        bool entry_page) {
    std::string full_path = web_root;
    if (!full_path.empty() && full_path[full_path.size() - 1] != '/') {
        full_path.push_back('/');
    }
    full_path.append(relative_path);

    int current_fd = open(web_root.c_str(), O_RDONLY | O_CLOEXEC | O_DIRECTORY | O_NOFOLLOW);
    if (current_fd < 0) {
        if (entry_page) {
            AIDEN_LOG_ERROR("static_assets", "web_root_open_failed", "path=%s error=%s",
                            web_root.c_str(), strerror(errno));
            return make_error(503, "Service Unavailable", "Service Unavailable");
        }
        return make_error(404, "Not Found", "Not Found");
    }

    int fd = -1;
    int open_error = 0;
    size_t offset = 0;
    while (offset < relative_path.size()) {
        const size_t slash = relative_path.find('/', offset);
        const bool final_segment = slash == std::string::npos;
        const std::string segment = relative_path.substr(
            offset, final_segment ? std::string::npos : slash - offset);
        const int flags = final_segment
            ? O_RDONLY | O_CLOEXEC | O_NOFOLLOW
            : O_RDONLY | O_CLOEXEC | O_DIRECTORY | O_NOFOLLOW;
        const int next_fd = openat(current_fd, segment.c_str(), flags);
        if (next_fd < 0) {
            open_error = errno;
        }
        close(current_fd);
        current_fd = -1;
        if (next_fd < 0) {
            break;
        }
        if (final_segment) {
            fd = next_fd;
            break;
        }
        current_fd = next_fd;
        offset = slash + 1;
    }
    if (current_fd >= 0) {
        close(current_fd);
    }
    if (fd < 0) {
        errno = open_error == 0 ? ENOENT : open_error;
        if (entry_page) {
            AIDEN_LOG_ERROR("static_assets", "page_open_failed", "path=%s error=%s",
                            full_path.c_str(), strerror(errno));
            return make_error(503, "Service Unavailable", "Service Unavailable");
        }
        return make_error(404, "Not Found", "Not Found");
    }

    struct stat st;
    const int stat_result = fstat(fd, &st);
    if (stat_result != 0 || !S_ISREG(st.st_mode) || st.st_size < 0) {
        const int saved_errno = stat_result != 0 ? errno : EINVAL;
        close(fd);
        if (entry_page) {
            AIDEN_LOG_ERROR("static_assets", "page_stat_failed", "path=%s error=%s",
                            full_path.c_str(), strerror(saved_errno));
            return make_error(503, "Service Unavailable", "Service Unavailable");
        }
        return make_error(404, "Not Found", "Not Found");
    }

    ConfigWebStaticAssetResponse response;
    response.handled = true;
    response.content_type = content_type_for_path(relative_path);
    response.body.clear();
    response.body_fd = fd;
    response.body_fd_size = static_cast<unsigned long long>(st.st_size);
    response.headers.push_back(std::make_pair("Cache-Control", "no-cache"));
    return response;
}

}  // namespace

ConfigWebStaticAssetResponse serve_config_web_static_asset(
    const std::string& web_root,
    const std::string& method,
    const std::string& request_path) {
    const bool entry_page = request_path == "/" || request_path == "/llm-logs";
    const bool asset_path = request_path.compare(0, 8, "/assets/") == 0;
    if (!entry_page && !asset_path) {
        return ConfigWebStaticAssetResponse();
    }

    if (method != "GET") {
        ConfigWebStaticAssetResponse response =
            make_error(405, "Method Not Allowed", "Method Not Allowed");
        response.headers.push_back(std::make_pair("Allow", "GET"));
        return response;
    }

    if (request_path == "/") {
        return open_asset(web_root, "index.html", true);
    }
    if (request_path == "/llm-logs") {
        return open_asset(web_root, "llm-logs.html", true);
    }

    std::string relative_path;
    if (!decode_asset_path(request_path, &relative_path)) {
        return make_error(404, "Not Found", "Not Found");
    }
    return open_asset(web_root, relative_path, false);
}

}  // namespace aiden
