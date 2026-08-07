#pragma once

#include "doctest.h"

#include <fstream>
#include <sstream>
#include <string>

inline std::string read_config_web_asset(const std::string& relative_path) {
    const std::string path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web/web/" + relative_path;
    std::ifstream in(path.c_str(), std::ios::binary);
    REQUIRE_MESSAGE(in.good(), "failed to read config web asset: " << relative_path);

    std::ostringstream buffer;
    buffer << in.rdbuf();
    return buffer.str();
}

inline std::string read_repository_file(const std::string& relative_path) {
    const std::string path = std::string(AIDEN_SOURCE_DIR) + "/" + relative_path;
    std::ifstream in(path.c_str(), std::ios::binary);
    REQUIRE_MESSAGE(in.good(), "failed to read repository file: " << relative_path);
    std::ostringstream buffer;
    buffer << in.rdbuf();
    return buffer.str();
}

inline std::string without_ascii_whitespace(const std::string& text) {
    std::string compact;
    compact.reserve(text.size());
    for (size_t i = 0; i < text.size(); ++i) {
        const char c = text[i];
        if (c != ' ' && c != '\t' && c != '\r' && c != '\n') {
            compact.push_back(c);
        }
    }
    return compact;
}

inline std::string read_config_web_config_scripts() {
    const char* paths[] = {
        "assets/js/config/state.js",
        "assets/js/config/api.js",
        "assets/js/config/i18n.js",
        "assets/js/config/test-toast.js",
        "assets/js/config/config-meta.js",
        "assets/js/config/config-form.js",
        "assets/js/config/providers.js",
        "assets/js/config/wifi.js",
        "assets/js/config/agent-status.js",
        "assets/js/config/logs.js",
        "assets/js/config/ota.js",
        "assets/js/config/storage.js",
        "assets/js/config/system-env.js",
        "assets/js/config/stt-test.js",
        "assets/js/config/app.js",
        nullptr,
    };
    std::string scripts;
    for (size_t i = 0; paths[i] != nullptr; ++i) {
        scripts += read_config_web_asset(paths[i]);
        scripts.push_back('\n');
    }
    return scripts;
}

inline std::string read_config_web_asset_bundle() {
    return read_config_web_asset("index.html") +
           read_config_web_asset("assets/css/common.css") +
           read_config_web_asset("assets/css/config.css") +
           read_config_web_config_scripts() +
           without_ascii_whitespace(read_config_web_config_scripts()) +
           read_repository_file("src/agent/internal/agent/config_meta.go");
}

inline std::string read_config_web_llm_asset_bundle() {
    return read_config_web_asset("llm-logs.html") +
           read_config_web_asset("assets/css/common.css") +
           read_config_web_asset("assets/css/llm-logs.css") +
           read_config_web_asset("assets/js/llm-logs.js");
}
