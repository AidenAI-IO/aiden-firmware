// End-to-end tests for the config_web HTTP server. These spawn the actual
// product binary against a stubbed `agent` CLI and probe the live HTTP API,
// catching the kind of cross-component contract bugs (status codes, JSON
// shape, fail-closed paths) that the source-grep tests in
// config_web_source_test.cpp cannot see.
//
// The test driver:
//   1. picks an ephemeral TCP port
//   2. writes scratch agent.toml / wifi.conf / system_env files
//   3. spawns config_web with --bind=127.0.0.1, AIDEN_AGENT_BIN pointing at
//      the stub agent, and the AIDEN_AGENT_STUB_* env knobs configured for
//      the scenario
//   4. polls the HTTP port until ready, then issues requests
//   5. reaps the child on teardown
//
// Required compile-time defines (set by tests/CMakeLists.txt):
//   AIDEN_CONFIG_WEB_BIN  absolute path to the freshly built config_web
//   AIDEN_AGENT_STUB_BIN  absolute path to the freshly built agent_stub

#include "doctest.h"

#include "cJSON/cJSON.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <memory>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

extern char** environ;

#ifndef AIDEN_CONFIG_WEB_BIN
#error "AIDEN_CONFIG_WEB_BIN must be set by the build system"
#endif
#ifndef AIDEN_AGENT_STUB_BIN
#error "AIDEN_AGENT_STUB_BIN must be set by the build system"
#endif

namespace {

// Pick an ephemeral TCP port by binding 127.0.0.1:0 and reading what the
// kernel assigned, then closing. The port is racy (something else may grab
// it before config_web starts) but in practice this is good enough for
// host CI; if it ever flakes we add a retry loop.
int pick_ephemeral_port() {
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    REQUIRE(fd >= 0);
    sockaddr_in addr;
    std::memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    addr.sin_port = 0;
    REQUIRE(::bind(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) == 0);
    socklen_t len = sizeof(addr);
    REQUIRE(::getsockname(fd, reinterpret_cast<sockaddr*>(&addr), &len) == 0);
    int port = ntohs(addr.sin_port);
    ::close(fd);
    return port;
}

std::string make_temp_dir() {
    char tmpl[] = "/tmp/aiden_config_web_e2e_XXXXXX";
    char* p = ::mkdtemp(tmpl);
    REQUIRE(p != nullptr);
    return std::string(p);
}

void write_file(const std::string& path, const std::string& content) {
    std::ofstream out(path);
    REQUIRE(out.good());
    out << content;
}

void write_binary_file(const std::string& path, const std::string& content) {
    std::ofstream out(path, std::ios::binary);
    REQUIRE(out.good());
    out.write(content.data(), static_cast<std::streamsize>(content.size()));
    REQUIRE(out.good());
}

std::string read_file(const std::string& path) {
    std::ifstream in(path);
    REQUIRE(in.good());
    std::ostringstream buf;
    buf << in.rdbuf();
    return buf.str();
}

std::string shell_quote(const std::string& text) {
    std::string out = "'";
    for (size_t i = 0; i < text.size(); ++i) {
        if (text[i] == '\'') {
            out += "'\\''";
        } else {
            out.push_back(text[i]);
        }
    }
    out.push_back('\'');
    return out;
}

std::string run_command_capture(const std::string& command) {
    FILE* pipe = ::popen(command.c_str(), "r");
    REQUIRE(pipe != nullptr);
    std::string output;
    char buffer[512];
    while (fgets(buffer, sizeof(buffer), pipe)) {
        output += buffer;
    }
    int status = ::pclose(pipe);
    REQUIRE(status >= 0);
    REQUIRE(WIFEXITED(status));
    REQUIRE(WEXITSTATUS(status) == 0);
    return output;
}

std::string print_json_unformatted(cJSON* root) {
    char* text = cJSON_PrintUnformatted(root);
    REQUIRE(text != nullptr);
    std::string result(text);
    free(text);
    return result;
}

std::string remove_top_level_key(std::string json, const char* key) {
    cJSON* root = cJSON_Parse(json.c_str());
    REQUIRE(root != nullptr);
    REQUIRE(cJSON_GetObjectItem(root, key) != nullptr);
    cJSON_DeleteItemFromObject(root, key);
    std::string result = print_json_unformatted(root);
    cJSON_Delete(root);
    return result;
}

std::string remove_nested_key(std::string json, const char* section, const char* key) {
    cJSON* root = cJSON_Parse(json.c_str());
    REQUIRE(root != nullptr);
    cJSON* section_obj = cJSON_GetObjectItem(root, section);
    REQUIRE(section_obj != nullptr);
    REQUIRE((section_obj->type & 0xff) == cJSON_Object);
    REQUIRE(cJSON_GetObjectItem(section_obj, key) != nullptr);
    cJSON_DeleteItemFromObject(section_obj, key);
    std::string result = print_json_unformatted(root);
    cJSON_Delete(root);
    return result;
}

std::string replace_top_level_key_with_array(std::string json, const char* key) {
    cJSON* root = cJSON_Parse(json.c_str());
    REQUIRE(root != nullptr);
    REQUIRE(cJSON_GetObjectItem(root, key) != nullptr);
    cJSON_DeleteItemFromObject(root, key);
    cJSON_AddItemToObject(root, key, cJSON_CreateArray());
    std::string result = print_json_unformatted(root);
    cJSON_Delete(root);
    return result;
}

std::string required_json_string(cJSON* object, const char* key) {
    cJSON* item = cJSON_GetObjectItem(object, key);
    REQUIRE(item != nullptr);
    REQUIRE(item->valuestring != nullptr);
    return std::string(item->valuestring);
}

int required_json_int(cJSON* object, const char* key) {
    cJSON* item = cJSON_GetObjectItem(object, key);
    REQUIRE(item != nullptr);
    REQUIRE((item->type & 0xff) == cJSON_Number);
    return item->valueint;
}

cJSON* required_test_result(cJSON* root, const char* check) {
    cJSON* results = cJSON_GetObjectItem(root, "results");
    REQUIRE(results != nullptr);
    REQUIRE((results->type & 0xff) == cJSON_Array);
    const int count = cJSON_GetArraySize(results);
    for (int i = 0; i < count; ++i) {
        cJSON* item = cJSON_GetArrayItem(results, i);
        REQUIRE(item != nullptr);
        cJSON* check_item = cJSON_GetObjectItem(item, "check");
        if (check_item != nullptr && check_item->valuestring != nullptr && std::strcmp(check_item->valuestring, check) == 0) {
            return item;
        }
    }
    return nullptr;
}

std::string replace_all(std::string text, const std::string& needle, const std::string& replacement) {
    size_t pos = 0;
    while ((pos = text.find(needle, pos)) != std::string::npos) {
        text.replace(pos, needle.size(), replacement);
        pos += replacement.size();
    }
    return text;
}

std::string resolved_config_json(const std::string& search_provider, bool search_has_api_key) {
    return std::string(
        "{"
        "\"model_providers\":{\"stub-openai\":{\"type\":\"openai\",\"api_key\":\"sk-stub-secret-1234\"},"
        "\"stub-ollama\":{\"type\":\"ollama\",\"base_url\":\"http://127.0.0.1:11434\"}},"
        "\"device\":{\"backend\":\"hdmi\",\"device_type\":\"iOS\"},"
        "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"bytedance-seed/seed-2.0-lite\","
        "\"temperature\":0.2,\"max_response_tokens\":1000,"
        "\"context_window\":0,\"model_max_output_tokens\":0},"
        "\"tts_providers\":{\"minimax-cn\":{\"type\":\"minimax-cn\"}},"
        "\"stt_providers\":{\"openai-whisper\":{\"type\":\"openai-whisper\"}},"
        "\"tts\":{\"provider\":\"minimax-cn\",\"api_key\":\"\",\"model\":\"\",\"voice_id\":\"male-qn-qingse\","
        "\"emotion\":\"happy\",\"speed\":1},"
        "\"stt\":{\"provider\":\"openai-whisper\",\"api_key\":\"\",\"model\":\"whisper-1\",\"base_url\":\"\","
        "\"app_id\":\"\",\"secret_id\":\"\",\"secret_key\":\"\",\"region\":\"\",\"engine_model_type\":\"\"},"
        "\"audio\":{\"socket\":\"/run/audio_service/audio_service.sock\",\"sample_rate\":16000,"
        "\"channels\":1,\"bit_width\":16,\"playback_backend\":\"audio_service\"},"
        "\"audio_archive\":{\"enabled\":true,\"max_files\":500,\"max_size_mb\":100,"
        "\"storage_path\":\"/userdata/audio\"},"
        "\"voice_notifications\":{\"enabled\":false,\"max_pending\":6,"
        "\"response_tail\":{\"enabled\":false,\"max_items\":1,\"max_text_chars\":72},"
        "\"expiration\":{\"default_ttl_seconds\":120,\"code_ttl_seconds\":{\"storage\":900}}},"
        "\"ota\":{\"github_proxy_url\":\"https://gh-proxy.com\"},"
        "\"hid\":{\"keyboard_device\":\"/dev/hidg0\",\"keyboard_layout\":\"qwerty\",\"mouse_device\":\"/dev/hidg1\","
        "\"android_keyboard_device\":\"/dev/hidg2\","
        "\"frame_socket\":\"/run/frame_service/frame_service.sock\",\"input_backend\":\"hid\"},"
        "\"search\":{\"provider\":\"") + search_provider + "\",\"has_api_key\":" +
        (search_has_api_key ? "true" : "false") +
        "},"
        "\"telemetry\":{\"enabled\":false,\"provider\":\"langfuse\",\"base_url\":\"\","
        "\"public_key\":\"\",\"secret_key\":\"\",\"upload_screenshots\":true,"
        "\"upload_timeout_sec\":30,\"max_retry\":2,\"tags\":[],\"environment\":\"default\"},"
        "\"agent\":{\"locale\":\"zh-CN\",\"custom_instruction\":\"stub custom instruction\",\"additional_prompt\":\"\","
        "\"input_mode\":\"text\",\"trigger_mode\":\"manual\",\"vad_backend\":\"rknn\","
        "\"vad_model_path\":\"/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn\","
        "\"vad_helper_path\":\"/oem/usr/bin/rknn_vad\",\"vad_speech_threshold\":0.5,"
        "\"silence_ms\":650,\"min_speech_ms\":300,\"voice_followup_enabled\":false,"
        "\"voice_followup_timeout_ms\":6000,\"voice_first_turn_timeout_ms\":10000,"
        "\"voice_max_turns\":0,\"voice_interrupt_on_wakeup\":true,"
        "\"voice_streaming_tts_enabled\":true,\"voice_tool_call_speech\":false,"
        "\"voice_max_response_tokens\":300,\"load_all_tools\":false,\"max_iterations\":-1,"
        "\"screenshot_keep_n\":3,"
        "\"screenshot_prune_interval\":25,\"screen_stable_timeout_ms\":3500,"
        "\"screen_stable_ms\":500,\"screen_stable_diff_threshold\":2}"
        "}\n";
}

bool wait_for_file_contains(const std::string& path, const std::string& needle, int timeout_ms) {
    using clock = std::chrono::steady_clock;
    auto deadline = clock::now() + std::chrono::milliseconds(timeout_ms);
    while (clock::now() < deadline) {
        std::ifstream in(path);
        if (in.good()) {
            std::ostringstream buf;
            buf << in.rdbuf();
            if (buf.str().find(needle) != std::string::npos) {
                return true;
            }
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(25));
    }
    return false;
}

bool wait_for_file_contains_any(const std::string& path,
                                const std::vector<std::string>& needles,
                                int timeout_ms) {
    using clock = std::chrono::steady_clock;
    auto deadline = clock::now() + std::chrono::milliseconds(timeout_ms);
    while (clock::now() < deadline) {
        std::ifstream in(path);
        if (in.good()) {
            std::ostringstream buf;
            buf << in.rdbuf();
            const std::string contents = buf.str();
            for (size_t i = 0; i < needles.size(); ++i) {
                if (contents.find(needles[i]) != std::string::npos) {
                    return true;
                }
            }
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(25));
    }
    return false;
}

bool wait_for_port(int port, int timeout_ms) {
    using clock = std::chrono::steady_clock;
    auto deadline = clock::now() + std::chrono::milliseconds(timeout_ms);
    while (clock::now() < deadline) {
        int fd = ::socket(AF_INET, SOCK_STREAM, 0);
        if (fd >= 0) {
            sockaddr_in addr;
            std::memset(&addr, 0, sizeof(addr));
            addr.sin_family = AF_INET;
            addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
            addr.sin_port = htons(static_cast<uint16_t>(port));
            int rc = ::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr));
            ::close(fd);
            if (rc == 0) {
                return true;
            }
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(50));
    }
    return false;
}

class HeadProbeServer {
public:
    HeadProbeServer() {
        fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        REQUIRE(fd_ >= 0);
        int opt = 1;
        REQUIRE(::setsockopt(fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt)) == 0);
        sockaddr_in addr;
        std::memset(&addr, 0, sizeof(addr));
        addr.sin_family = AF_INET;
        addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        addr.sin_port = 0;
        REQUIRE(::bind(fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) == 0);
        socklen_t len = sizeof(addr);
        REQUIRE(::getsockname(fd_, reinterpret_cast<sockaddr*>(&addr), &len) == 0);
        port_ = ntohs(addr.sin_port);
        REQUIRE(::listen(fd_, 8) == 0);
        worker_ = std::thread([this] { serve(); });
    }

    ~HeadProbeServer() {
        stop_ = true;
        if (fd_ >= 0) {
            ::shutdown(fd_, SHUT_RDWR);
            ::close(fd_);
        }
        if (worker_.joinable()) {
            worker_.join();
        }
    }

    int port() const { return port_; }

    std::vector<std::string> requests() const {
        std::lock_guard<std::mutex> lock(mu_);
        return requests_;
    }

private:
    void serve() {
        while (!stop_) {
            int client = ::accept(fd_, nullptr, nullptr);
            if (client < 0) {
                continue;
            }
            std::string request;
            char buf[4096];
            while (request.find("\r\n\r\n") == std::string::npos) {
                ssize_t n = ::recv(client, buf, sizeof(buf), 0);
                if (n <= 0) {
                    break;
                }
                request.append(buf, static_cast<size_t>(n));
            }
            {
                std::lock_guard<std::mutex> lock(mu_);
                requests_.push_back(request);
            }
            const char response[] =
                "HTTP/1.1 200 OK\r\n"
                "Content-Length: 0\r\n"
                "Connection: close\r\n"
                "\r\n";
            (void)::send(client, response, sizeof(response) - 1, 0);
            ::close(client);
        }
    }

    int fd_ = -1;
    int port_ = 0;
    mutable std::mutex mu_;
    std::atomic<bool> stop_{false};
    std::vector<std::string> requests_;
    std::thread worker_;
};

struct CapturedHTTPRequest {
    std::string method;
    std::string path;
    std::string body;
};

class StubAgentHTTPServer {
public:
    explicit StubAgentHTTPServer(int stt_stop_status = 200)
        : stt_stop_status_(stt_stop_status) {
        fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        REQUIRE(fd_ >= 0);
        int opt = 1;
        REQUIRE(::setsockopt(fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt)) == 0);
        sockaddr_in addr;
        std::memset(&addr, 0, sizeof(addr));
        addr.sin_family = AF_INET;
        addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        addr.sin_port = 0;
        REQUIRE(::bind(fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) == 0);
        socklen_t len = sizeof(addr);
        REQUIRE(::getsockname(fd_, reinterpret_cast<sockaddr*>(&addr), &len) == 0);
        port_ = ntohs(addr.sin_port);
        REQUIRE(::listen(fd_, 8) == 0);
        worker_ = std::thread([this] { serve(); });
    }

    ~StubAgentHTTPServer() {
        stop_ = true;
        if (fd_ >= 0) {
            ::shutdown(fd_, SHUT_RDWR);
            ::close(fd_);
        }
        if (worker_.joinable()) {
            worker_.join();
        }
    }

    int port() const { return port_; }

    std::vector<CapturedHTTPRequest> requests() const {
        std::lock_guard<std::mutex> lock(mu_);
        return requests_;
    }

private:
    std::string build_response(const std::string& path) const {
        // Echo the request target back so callers can assert the query string
        // survived the proxy hop: config_web strips the query off the path when
        // parsing the request line and must forward it from the saved copy.
        if (path.compare(0, 12, "/api/models?") == 0 || path == "/api/models") {
            const std::string body =
                std::string("{\"request_target\":\"") + path + "\",\"models\":[]}";
            std::ostringstream out;
            out << "HTTP/1.1 200 OK\r\n"
                << "Content-Type: application/json\r\n"
                << "Content-Length: " << body.size() << "\r\n"
                << "Connection: close\r\n"
                << "\r\n"
                << body;
            return out.str();
        }
        if (path == "/api/config-test/stt/start") {
            const std::string body = "{\"ok\":true,\"status\":\"recording\",\"sample_rate\":16000}";
            std::ostringstream out;
            out << "HTTP/1.1 200 OK\r\n"
                << "Content-Type: application/json\r\n"
                << "Content-Length: " << body.size() << "\r\n"
                << "Connection: close\r\n"
                << "\r\n"
                << body;
            return out.str();
        }
        if (path == "/api/storage/eject") {
            const std::string body =
                "{\"effective_mode\":1,"
                "\"card\":{\"present\":true,\"mounted\":false},"
                "\"format_job\":{\"status\":\"idle\"}}";
            std::ostringstream out;
            out << "HTTP/1.1 200 OK\r\n"
                << "Content-Type: application/json\r\n"
                << "Content-Length: " << body.size() << "\r\n"
                << "Connection: close\r\n"
                << "\r\n"
                << body;
            return out.str();
        }
        if (path == "/api/config-test/stt/stop") {
            const bool ok = stt_stop_status_ >= 200 && stt_stop_status_ < 300;
            const std::string body = ok
                ? "{\"ok\":true,\"transcript\":\"agent live transcript\","
                  "\"results\":[{\"check\":\"stt_transcription\",\"passed\":true,"
                  "\"detail\":\"transcribed live recording with tencent-asr via streaming upload\"}]}"
                : "{\"ok\":false,\"error\":\"stop failed\"}";
            std::ostringstream out;
            out << "HTTP/1.1 " << stt_stop_status_ << (ok ? " OK\r\n" : " Service Unavailable\r\n")
                << "Content-Type: application/json\r\n"
                << "Content-Length: " << body.size() << "\r\n"
                << "Connection: close\r\n"
                << "\r\n"
                << body;
            return out.str();
        }
        const std::string body = "not found";
        std::ostringstream out;
        out << "HTTP/1.1 404 Not Found\r\n"
            << "Content-Type: text/plain\r\n"
            << "Content-Length: " << body.size() << "\r\n"
            << "Connection: close\r\n"
            << "\r\n"
            << body;
        return out.str();
    }

    void serve() {
        while (!stop_) {
            int client = ::accept(fd_, nullptr, nullptr);
            if (client < 0) {
                continue;
            }

            std::string raw;
            char buf[4096];
            size_t header_end = std::string::npos;
            size_t content_length = 0;
            while (true) {
                ssize_t n = ::recv(client, buf, sizeof(buf), 0);
                if (n <= 0) {
                    break;
                }
                raw.append(buf, static_cast<size_t>(n));
                if (header_end == std::string::npos) {
                    header_end = raw.find("\r\n\r\n");
                    if (header_end != std::string::npos) {
                        const std::string headers = raw.substr(0, header_end);
                        const std::string needle = "Content-Length:";
                        size_t pos = headers.find(needle);
                        if (pos != std::string::npos) {
                            pos += needle.size();
                            while (pos < headers.size() && headers[pos] == ' ') pos++;
                            content_length = static_cast<size_t>(std::strtoul(headers.c_str() + pos, nullptr, 10));
                        }
                    }
                }
                if (header_end != std::string::npos) {
                    const size_t have_body = raw.size() - (header_end + 4);
                    if (have_body >= content_length) {
                        break;
                    }
                }
            }

            std::string method;
            std::string path;
            std::string body;
            const size_t line_end = raw.find("\r\n");
            if (line_end != std::string::npos) {
                const std::string line = raw.substr(0, line_end);
                const size_t sp1 = line.find(' ');
                const size_t sp2 = sp1 == std::string::npos ? std::string::npos : line.find(' ', sp1 + 1);
                if (sp1 != std::string::npos && sp2 != std::string::npos) {
                    method = line.substr(0, sp1);
                    path = line.substr(sp1 + 1, sp2 - sp1 - 1);
                }
            }
            if (header_end != std::string::npos) {
                body = raw.substr(header_end + 4);
                if (body.size() > content_length) {
                    body.resize(content_length);
                }
            }

            {
                std::lock_guard<std::mutex> lock(mu_);
                requests_.push_back(CapturedHTTPRequest{method, path, body});
            }

            const std::string response = build_response(path);
            (void)::send(client, response.data(), response.size(), 0);
            ::close(client);
        }
    }

    int fd_ = -1;
    int port_ = 0;
    int stt_stop_status_ = 200;
    mutable std::mutex mu_;
    std::atomic<bool> stop_{false};
    std::vector<CapturedHTTPRequest> requests_;
    std::thread worker_;
};

// Captures one HTTP response. Status code is parsed from the status line; the
// body is the bytes after the first blank line. We don't care about
// chunked/keep-alive since config_web closes the connection per response.
struct HttpResponse {
    int status = 0;
    std::string headers;
    std::string body;
};

std::string response_header(const HttpResponse& response, const std::string& name) {
    const std::string needle = name + ":";
    size_t pos = response.headers.find(needle);
    if (pos == std::string::npos) return "";
    pos += needle.size();
    while (pos < response.headers.size() && response.headers[pos] == ' ') ++pos;
    size_t end = response.headers.find("\r\n", pos);
    return response.headers.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
}

// PLACEHOLDER_HTTP
HttpResponse http_request(int port, const std::string& method, const std::string& path,
                          const std::string& body = "") {
    HttpResponse resp;
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    REQUIRE(fd >= 0);
    sockaddr_in addr;
    std::memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    addr.sin_port = htons(static_cast<uint16_t>(port));
    REQUIRE(::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) == 0);

    std::ostringstream req;
    req << method << " " << path << " HTTP/1.1\r\n"
        << "Host: 127.0.0.1\r\n"
        << "Connection: close\r\n";
    if (!body.empty()) {
        req << "Content-Type: application/json\r\n"
            << "Content-Length: " << body.size() << "\r\n";
    }
    req << "\r\n" << body;
    std::string raw = req.str();

    size_t sent = 0;
    while (sent < raw.size()) {
        ssize_t n = ::send(fd, raw.data() + sent, raw.size() - sent, 0);
        REQUIRE(n > 0);
        sent += static_cast<size_t>(n);
    }

    std::string buf;
    char chunk[4096];
    while (true) {
        ssize_t n = ::recv(fd, chunk, sizeof(chunk), 0);
        if (n <= 0) break;
        buf.append(chunk, static_cast<size_t>(n));
    }
    ::close(fd);

    // Parse "HTTP/1.1 NNN ...\r\n"
    size_t sp1 = buf.find(' ');
    if (sp1 != std::string::npos) {
        size_t sp2 = buf.find(' ', sp1 + 1);
        if (sp2 != std::string::npos) {
            resp.status = std::atoi(buf.substr(sp1 + 1, sp2 - sp1 - 1).c_str());
        }
    }
    size_t sep = buf.find("\r\n\r\n");
    if (sep != std::string::npos) {
        resp.headers = buf.substr(0, sep + 2);
        resp.body = buf.substr(sep + 4);
    }
    return resp;
}

// Spawns config_web with the agent stub injected and the chosen STUB env vars
// already exported. Returns the child pid; caller is responsible for SIGTERM
// + waitpid via terminate_server().
struct ServerHandle {
    pid_t pid = -1;
    int port = 0;
    std::string tmp_dir;
    std::string web_root;

    ~ServerHandle() {
        if (pid > 0) {
            ::kill(pid, SIGTERM);
            int status = 0;
            for (int i = 0; i < 20 && ::waitpid(pid, &status, WNOHANG) == 0; ++i) {
                std::this_thread::sleep_for(std::chrono::milliseconds(50));
            }
            if (::waitpid(pid, &status, WNOHANG) == 0) {
                ::kill(pid, SIGKILL);
                ::waitpid(pid, &status, 0);
            }
        }
        if (!tmp_dir.empty()) {
            // Best-effort recursive cleanup. We only created a handful of
            // files; rm -rf via `unlink` is fine.
            std::string cmd = "rm -rf '" + tmp_dir + "'";
            (void)std::system(cmd.c_str());
        }
    }
};

struct StubEnv {
    // Each entry is "KEY=VALUE" or "KEY=" for empty. We pass these as exec
    // env vars; see start_server().
    std::vector<std::string> entries;
    bool has_agent_bin = false;
    // When non-empty, written to <tmp>/storage.state and passed to config_web
    // via --storage-state so storage endpoints see a controlled snapshot.
    std::string storage_state;

    void set(const std::string& key, const std::string& value) {
        if (key == "AIDEN_AGENT_BIN") has_agent_bin = true;
        entries.push_back(key + "=" + value);
    }
};

std::unique_ptr<ServerHandle> start_server(const StubEnv& stub_env,
                                           bool include_index = true,
                                           bool include_llm_logs = true) {
    auto handle = std::unique_ptr<ServerHandle>(new ServerHandle());
    handle->tmp_dir = make_temp_dir();
    handle->port = pick_ephemeral_port();
    handle->web_root = handle->tmp_dir + "/config-web";

    const std::string agent_toml_path = handle->tmp_dir + "/agent.toml";
    const std::string wifi_conf_path = handle->tmp_dir + "/wifi.conf";
    const std::string sysenv_path = handle->tmp_dir + "/system_env";
    const std::string ota_state_path = handle->tmp_dir + "/ota_state.json";
    const std::string cmdline_path = handle->tmp_dir + "/cmdline";
    write_file(agent_toml_path, "");
    write_file(wifi_conf_path, "");
    write_file(sysenv_path, "");
    write_file(cmdline_path, "console=ttyFIQ0 aiden.slot_suffix=_a root=PARTLABEL=rootfs_a\n");
    REQUIRE(::mkdir(handle->web_root.c_str(), 0755) == 0);
    REQUIRE(::mkdir((handle->web_root + "/assets").c_str(), 0755) == 0);
    REQUIRE(::mkdir((handle->web_root + "/assets/css").c_str(), 0755) == 0);
    REQUIRE(::mkdir((handle->web_root + "/assets/js").c_str(), 0755) == 0);
    if (include_index) write_file(handle->web_root + "/index.html", "test index page");
    if (include_llm_logs) write_file(handle->web_root + "/llm-logs.html", "test llm logs page");
    write_file(handle->web_root + "/assets/css/test.css", "body{color:#123456}");
    write_file(handle->web_root + "/assets/js/test.js", "globalThis.configWebTest=true;");
    write_file(handle->web_root + "/assets/test.json", "{}");
    write_file(handle->web_root + "/assets/test.svg", "<svg/>");
    write_binary_file(handle->web_root + "/assets/test.png", "png");
    write_binary_file(handle->web_root + "/assets/test.ico", "ico");
    if (!stub_env.storage_state.empty()) {
        write_file(handle->tmp_dir + "/storage.state", stub_env.storage_state);
    }

    pid_t pid = ::fork();
    REQUIRE(pid >= 0);

    if (pid == 0) {
        // Child: redirect stdio to /dev/null so a noisy server doesn't drown
        // the test runner output.
        int devnull = ::open("/dev/null", O_RDWR);
        if (devnull >= 0) {
            ::dup2(devnull, STDIN_FILENO);
            ::dup2(devnull, STDOUT_FILENO);
            ::dup2(devnull, STDERR_FILENO);
            if (devnull > 2) ::close(devnull);
        }
        // Build env: inherit current env, then overlay AIDEN_AGENT_BIN +
        // STUB knobs. If the scenario already provides AIDEN_AGENT_BIN
        // (e.g. to point at a non-existent path), we skip the default
        // injection -- getenv() returns the first match, so a default that
        // appeared earlier would shadow the test's override.
        std::vector<std::string> env_storage;
        for (char** e = environ; *e; ++e) {
            env_storage.push_back(std::string(*e));
        }
        if (!stub_env.has_agent_bin) {
            env_storage.push_back(std::string("AIDEN_AGENT_BIN=") + AIDEN_AGENT_STUB_BIN);
        }
        for (const auto& kv : stub_env.entries) {
            env_storage.push_back(kv);
        }
        std::vector<char*> envp;
        envp.reserve(env_storage.size() + 1);
        for (auto& s : env_storage) envp.push_back(const_cast<char*>(s.c_str()));
        envp.push_back(nullptr);

        const std::string port_arg = "--port=" + std::to_string(handle->port);
        const std::string config_arg = "--config=" + agent_toml_path;
        const std::string wifi_arg = "--wifi-config=" + wifi_conf_path;
        const std::string sysenv_arg = "--system-env=" + sysenv_path;
        const std::string ota_state_arg = "--ota-state=" + ota_state_path;
        const std::string cmdline_arg = "--cmdline=" + cmdline_path;
        // Point --storage-state at a file inside tmp_dir either way; scenarios
        // without storage_state content get a missing file, which the status
        // endpoint must report as unavailable rather than erroring.
        const std::string storage_state_arg = "--storage-state=" + handle->tmp_dir + "/storage.state";
        const std::string web_root_arg = "--web-root=" + handle->web_root;
        std::vector<char*> argv = {
            const_cast<char*>(AIDEN_CONFIG_WEB_BIN),
            const_cast<char*>("--bind=127.0.0.1"),
            const_cast<char*>(port_arg.c_str()),
            const_cast<char*>(config_arg.c_str()),
            const_cast<char*>(wifi_arg.c_str()),
            const_cast<char*>(sysenv_arg.c_str()),
            const_cast<char*>(ota_state_arg.c_str()),
            const_cast<char*>(cmdline_arg.c_str()),
            const_cast<char*>(storage_state_arg.c_str()),
            const_cast<char*>(web_root_arg.c_str()),
            nullptr,
        };
        ::execve(AIDEN_CONFIG_WEB_BIN, argv.data(), envp.data());
        ::_exit(127);
    }

    handle->pid = pid;
    REQUIRE(wait_for_port(handle->port, 5000));
    return handle;
}

}  // namespace

// ----- end-to-end scenarios -----------------------------------------------
//
// Each TEST_CASE spawns a fresh config_web with a stub agent configured for
// the scenario, then asserts the HTTP behaviour. Status codes are the main
// contract we guard: a missing dependency MUST surface as 503 (server-side
// outage), a CLI rejection MUST surface as 400 (user input).

TEST_CASE("config_web: serves entry pages and static assets from web root") {
    StubEnv env;
    auto handle = start_server(env);
    HttpResponse index = http_request(handle->port, "GET", "/");
    CHECK(index.status == 200);
    CHECK(index.body == "test index page");
    CHECK(response_header(index, "Content-Type") == "text/html; charset=utf-8");
    CHECK(response_header(index, "Cache-Control") == "no-cache");

    HttpResponse logs = http_request(handle->port, "GET", "/llm-logs");
    CHECK(logs.status == 200);
    CHECK(logs.body == "test llm logs page");

    HttpResponse css = http_request(handle->port, "GET", "/assets/css/test.css");
    CHECK(css.status == 200);
    CHECK(response_header(css, "Content-Type") == "text/css; charset=utf-8");
    HttpResponse js = http_request(handle->port, "GET", "/assets/js/test.js");
    CHECK(js.status == 200);
    CHECK(response_header(js, "Content-Type") == "application/javascript; charset=utf-8");
    CHECK(response_header(http_request(handle->port, "GET", "/assets/test.json"), "Content-Type") ==
          "application/json; charset=utf-8");
    CHECK(response_header(http_request(handle->port, "GET", "/assets/test.svg"), "Content-Type") ==
          "image/svg+xml");
    CHECK(response_header(http_request(handle->port, "GET", "/assets/test.png"), "Content-Type") ==
          "image/png");
    CHECK(response_header(http_request(handle->port, "GET", "/assets/test.ico"), "Content-Type") ==
          "image/x-icon");
}

TEST_CASE("config_web: static assets fail closed without affecting APIs") {
    StubEnv env;
    auto handle = start_server(env, false, true);
    write_file(handle->tmp_dir + "/outside-secret.txt", "must not be served");

    CHECK(http_request(handle->port, "GET", "/").status == 503);
    CHECK(http_request(handle->port, "GET", "/assets/missing.css").status == 404);
    HttpResponse traversal =
        http_request(handle->port, "GET", "/assets/%2e%2e/outside-secret.txt");
    CHECK(traversal.status == 404);
    CHECK(traversal.body.find("must not be served") == std::string::npos);
    CHECK(http_request(handle->port, "GET", "/assets//test.css").status == 404);
    CHECK(http_request(handle->port, "GET", "/assets/%ZZ").status == 404);
    CHECK(http_request(handle->port, "GET", "/assets/%2fetc").status == 404);
    CHECK(http_request(handle->port, "GET", "/assets/%5ctest.css").status == 404);
    CHECK(http_request(handle->port, "POST", "/assets/js/test.js").status == 405);

    HttpResponse api = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(api.status == 200);
}

TEST_CASE("config_web: missing llm logs entry and symlink assets fail closed") {
    StubEnv env;
    auto handle = start_server(env, true, false);
    const std::string outside = handle->tmp_dir + "/outside.css";
    write_file(outside, "outside");
    REQUIRE(::symlink(outside.c_str(), (handle->web_root + "/assets/link.css").c_str()) == 0);

    CHECK(http_request(handle->port, "GET", "/llm-logs").status == 503);
    CHECK(http_request(handle->port, "GET", "/assets/link.css").status == 404);
    CHECK(http_request(handle->port, "POST", "/").status == 405);
    CHECK(http_request(handle->port, "POST", "/llm-logs").status == 405);
}

TEST_CASE("config_web: GET /api/config/meta returns 200 + parseable JSON when stub agent works") {
    StubEnv env;  // defaults: meta returns minimal valid JSON
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(resp.status == 200);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* sections = cJSON_GetObjectItem(parsed, "sections");
    REQUIRE(sections != nullptr);
    CHECK((sections->type & 0xff) == cJSON_Array);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config reads resolved config from agent") {
    StubEnv env;
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* hid = cJSON_GetObjectItem(config, "hid");
    REQUIRE(hid != nullptr);
    cJSON* frame_socket = cJSON_GetObjectItem(hid, "frame_socket");
    REQUIRE(frame_socket != nullptr);
    REQUIRE(frame_socket->valuestring != nullptr);
    CHECK(std::string(frame_socket->valuestring) == "/run/frame_service/frame_service.sock");
    cJSON* keyboard_layout = cJSON_GetObjectItem(hid, "keyboard_layout");
    REQUIRE(keyboard_layout != nullptr);
    REQUIRE(keyboard_layout->valuestring != nullptr);
    CHECK(std::string(keyboard_layout->valuestring) == "qwerty");

    cJSON* model = cJSON_GetObjectItem(config, "model");
    REQUIRE(model != nullptr);
    cJSON* provider = cJSON_GetObjectItem(model, "provider");
    REQUIRE(provider != nullptr);
    REQUIRE(provider->valuestring != nullptr);
    CHECK(std::string(provider->valuestring) == "openrouter");

    cJSON* audio_archive = cJSON_GetObjectItem(config, "audio_archive");
    REQUIRE(audio_archive != nullptr);
    cJSON* enabled = cJSON_GetObjectItem(audio_archive, "enabled");
    REQUIRE(enabled != nullptr);
    CHECK((enabled->type & 0xff) == cJSON_True);

    cJSON* voice_notifications = cJSON_GetObjectItem(config, "voice_notifications");
    REQUIRE(voice_notifications != nullptr);
    CHECK(cJSON_GetObjectItem(voice_notifications, "default_locale") == nullptr);
    CHECK(required_json_int(voice_notifications, "max_pending") == 8);
    cJSON* response_tail = cJSON_GetObjectItem(voice_notifications, "response_tail");
    REQUIRE(response_tail != nullptr);
    CHECK(required_json_int(response_tail, "max_text_chars") == 40);

    cJSON* agent = cJSON_GetObjectItem(config, "agent");
    REQUIRE(agent != nullptr);
    cJSON* custom_instruction = cJSON_GetObjectItem(agent, "custom_instruction");
    REQUIRE(custom_instruction != nullptr);
    REQUIRE(custom_instruction->valuestring != nullptr);
    CHECK(std::string(custom_instruction->valuestring) == "stub custom instruction");
    cJSON* locale = cJSON_GetObjectItem(agent, "locale");
    REQUIRE(locale != nullptr);
    REQUIRE(locale->valuestring != nullptr);
    CHECK(std::string(locale->valuestring) == "zh-CN");
    CHECK(cJSON_GetObjectItem(agent, "instruction") == nullptr);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config omits saved wifi credentials") {
    StubEnv env;
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/wifi.conf",
        "ctrl_interface=/var/run/wpa_supplicant\n"
        "update_config=1\n"
        "country=CN\n\n"
        "network={\n"
        "    ssid=746573742d77696669\n"
        "    psk=\"saved-wifi-secret\"\n"
        "    priority=7\n"
        "}\n");

    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("saved-wifi-secret") == std::string::npos);

    cJSON* root = cJSON_Parse(resp.body.c_str());
    REQUIRE(root != nullptr);
    cJSON* wifi = cJSON_GetObjectItem(root, "wifi");
    REQUIRE(wifi != nullptr);
    CHECK(cJSON_GetObjectItem(wifi, "psk") == nullptr);
    cJSON* networks = cJSON_GetObjectItem(wifi, "networks");
    REQUIRE(networks != nullptr);
    cJSON* network = cJSON_GetArrayItem(networks, 0);
    REQUIRE(network != nullptr);
    CHECK(cJSON_GetObjectItem(network, "psk") == nullptr);
    cJSON_Delete(root);
}

TEST_CASE("config_web: PUT /api/config/locale updates only the device locale") {
    StubEnv env;
    auto handle = start_server(env);
    const std::string original =
        "# Keep comments and fields owned by the Go agent.\n"
        "\"locale\" = \"zh-CN\"  # setup language\n"
        "custom_instruction = \"Keep this instruction.\"\n"
        "skills_dirs = [\"/userdata/skills\"]\n"
        "future_plugin_flag = true\n"
        "\n"
        "[device]\n"
        "backend = \"hdmi\"\n"
        "future_device_option = \"keep me\"\n";
    write_file(handle->tmp_dir + "/agent.toml", original);

    HttpResponse resp = http_request(handle->port, "PUT", "/api/config/locale",
                                     "{\"locale\":\"en-US\"}");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* locale = cJSON_GetObjectItem(parsed, "locale");
    REQUIRE(locale != nullptr);
    REQUIRE(locale->valuestring != nullptr);
    CHECK(std::string(locale->valuestring) == "en-US");
    cJSON* restart = cJSON_GetObjectItem(parsed, "agent_restart_scheduled");
    REQUIRE(restart != nullptr);
    CHECK((restart->type & 0xff) == cJSON_True);
    cJSON_Delete(parsed);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    std::string expected = original;
    const size_t locale_pos = expected.find("\"zh-CN\"");
    REQUIRE(locale_pos != std::string::npos);
    expected.replace(locale_pos, std::strlen("\"zh-CN\""), "\"en-US\"");
    CHECK(saved == expected);
}

TEST_CASE("config_web: PUT /api/config/locale inserts a missing locale before sections") {
    StubEnv env;
    auto handle = start_server(env);
    const std::string original =
        "# Existing top-level config stays in place.\n"
        "future_agent_option = 4\n"
        "custom_instruction = \"\"\"\n"
        "locale = \"this is instruction text\"\n"
        "[not-a-real-section]\n"
        "\"\"\"\n"
        "\n"
        "[device]\n"
        "backend = \"hdmi\"\n";
    write_file(handle->tmp_dir + "/agent.toml", original);

    HttpResponse resp = http_request(handle->port, "PUT", "/api/config/locale",
                                     "{\"locale\":\"en-US\"}");
    CHECK(resp.status == 200);

    CHECK(read_file(handle->tmp_dir + "/agent.toml") ==
          "# Existing top-level config stays in place.\n"
          "future_agent_option = 4\n"
          "custom_instruction = \"\"\"\n"
          "locale = \"this is instruction text\"\n"
          "[not-a-real-section]\n"
          "\"\"\"\n"
          "\n"
          "locale = \"en-US\"\n"
          "[device]\n"
          "backend = \"hdmi\"\n");
}

TEST_CASE("config_web: PUT /api/config/locale rejects unsupported locales without saving") {
    StubEnv env;
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml", "locale = \"zh-CN\"\n");

    HttpResponse resp = http_request(handle->port, "PUT", "/api/config/locale",
                                     "{\"locale\":\"fr-FR\"}");
    CHECK(resp.status == 400);
    CHECK(resp.body.find("unsupported locale") != std::string::npos);
    CHECK(read_file(handle->tmp_dir + "/agent.toml").find("locale = \"zh-CN\"") != std::string::npos);
}

TEST_CASE("config_web: PUT /api/config/locale preserves the confirmed locale when validation fails") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/check.json",
               "{\"valid\":false,\"errors\":[{\"field\":\"locale\",\"message\":\"stub locale rejection\"}]}\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CHECK_FILE", tmp + "/check.json");
    env.set("AIDEN_AGENT_STUB_CHECK_EXIT", "1");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml", "locale = \"zh-CN\"\n");

    HttpResponse resp = http_request(handle->port, "PUT", "/api/config/locale",
                                     "{\"locale\":\"en-US\"}");
    CHECK(resp.status == 400);
    CHECK(resp.body.find("stub locale rejection") != std::string::npos);
    CHECK(read_file(handle->tmp_dir + "/agent.toml").find("locale = \"zh-CN\"") != std::string::npos);
}

TEST_CASE("config_web: GET /api/config reports running OTA target version when health failed") {
    StubEnv env;
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/cmdline", "console=ttyFIQ0 aiden.slot_suffix=_b root=PARTLABEL=rootfs_b\n");
    write_file(handle->tmp_dir + "/ota_state.json",
        "{"
        "\"phase\":\"health\","
        "\"current_version\":\"20260601-100000-old\","
        "\"current_build_time\":\"2026-06-01T10:00:00Z\","
        "\"target_version\":\"20260602-100000-new\","
        "\"target_build_time\":\"2026-06-02T10:00:00Z\","
        "\"active_slot\":0,"
        "\"target_slot\":1,"
        "\"last_committed_version\":\"20260601-100000-old\","
        "\"last_committed_build_time\":\"2026-06-01T10:00:00Z\","
        "\"last_error\":\"health timeout waiting for /userdata/ota/health.ok\""
        "}\n");

    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* firmware = cJSON_GetObjectItem(parsed, "firmware");
    REQUIRE(firmware != nullptr);
    CHECK(required_json_string(firmware, "version") == "20260602-100000-new");
    CHECK(required_json_string(firmware, "build_time") == "2026-06-02T10:00:00Z");
    CHECK(required_json_string(firmware, "phase") == "health");
    CHECK(required_json_string(firmware, "health_status") == "failed");
    CHECK(required_json_string(firmware, "health_error") == "health timeout waiting for /userdata/ota/health.ok");
    CHECK(required_json_string(firmware, "previous_version") == "20260601-100000-old");
    CHECK(required_json_string(firmware, "running_slot") == "b");
    CHECK(required_json_string(firmware, "target_slot") == "b");
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: POST /api/system/env saves env without OTA restart") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string env_body = "HTTP_PROXY=http://proxy.example:18080\\n";
    const std::string body = "{\"system_env\":\"" + env_body + "\"}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/system/env", body);
    REQUIRE(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* agent_restart = cJSON_GetObjectItem(parsed, "agent_restart_scheduled");
    REQUIRE(agent_restart != nullptr);
    CHECK((agent_restart->type & 0xff) == cJSON_True);
    cJSON* ota_restart = cJSON_GetObjectItem(parsed, "ota_restart_scheduled");
    REQUIRE(ota_restart != nullptr);
    CHECK((ota_restart->type & 0xff) == cJSON_False);
    cJSON* saved_env = cJSON_GetObjectItem(parsed, "system_env");
    REQUIRE(saved_env != nullptr);
    REQUIRE(saved_env->valuestring != nullptr);
    CHECK(std::string(saved_env->valuestring) == "HTTP_PROXY=http://proxy.example:18080\n");
    cJSON_Delete(parsed);

    std::ifstream saved_in((handle->tmp_dir + "/system_env").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    CHECK(saved_buffer.str() == "HTTP_PROXY=http://proxy.example:18080\n");
}

TEST_CASE("config_web: POST /api/config writes audio_archive section") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"audio_archive\":{\"enabled\":false,\"max_files\":123,\"max_size_mb\":45,"
        "\"storage_path\":\"/userdata/custom-audio\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    const std::string saved = saved_buffer.str();
    CHECK(saved.find("[audio_archive]") != std::string::npos);
    CHECK(saved.find("enabled = false") != std::string::npos);
    CHECK(saved.find("max_files = 123") != std::string::npos);
    CHECK(saved.find("max_size_mb = 45") != std::string::npos);
    CHECK(saved.find("storage_path = \"/userdata/custom-audio\"") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config ignores model base_url for every provider") {
    const char* providers[] = {
        "openai", "anthropic", "ollama", "openrouter", "kimi", "kimi-cn",
        "volcengine", "fake",
    };

    for (const char* provider : providers) {
        StubEnv env;
        auto handle = start_server(env);

        const std::string base_url = "https://gateway.example.com/v1";
        const std::string body =
            std::string("{\"config\":{\"model\":{\"provider\":\"") + provider +
            "\",\"model\":\"x\",\"api_key\":\"k\",\"base_url\":\"" + base_url + "\"},"
            "\"hid\":{\"pointer_mode\":\"absolute\"},"
            "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        CHECK_MESSAGE(resp.status == 200, std::string(provider));

        const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
        CHECK_MESSAGE(saved.find(base_url) == std::string::npos, std::string(provider));
    }
}

TEST_CASE("config_web: POST /api/config writes named providers") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"my-openai\":{\"type\":\"openai\",\"api_key\":\"sk-plain-secret-1234\"},"
        "\"my-ollama\":{\"type\":\"ollama\",\"base_url\":\"http://127.0.0.1:11434\"}},"
        "\"model\":{\"provider\":\"my-openai\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.my-openai]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-plain-secret-1234\"") != std::string::npos);
    CHECK(saved.find("[model_providers.my-ollama]") != std::string::npos);
    CHECK(saved.find("base_url = \"http://127.0.0.1:11434\"") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config writes canonical provider maps and type fields") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{"
        "\"model_providers\":{\"my-openai\":{\"type\":\"openai\",\"api_key\":\"sk-model\"}},"
        "\"tts_providers\":{\"voice\":{\"type\":\"fish-audio\",\"api_key\":\"sk-tts\"}},"
        "\"stt_providers\":{\"transcriber\":{\"type\":\"openai-whisper\",\"api_key\":\"sk-stt\"}},"
        "\"model\":{\"provider\":\"my-openai\",\"model\":\"x\"},"
        "\"tts\":{\"provider\":\"voice\",\"speed\":1},"
        "\"stt\":{\"provider\":\"transcriber\",\"language\":\"zh\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.my-openai]") != std::string::npos);
    CHECK(saved.find("[tts_providers.voice]") != std::string::npos);
    CHECK(saved.find("[stt_providers.transcriber]") != std::string::npos);
    CHECK(saved.find("type = \"openai\"") != std::string::npos);
    CHECK(saved.find("type = \"fish-audio\"") != std::string::npos);
    CHECK(saved.find("type = \"openai-whisper\"") != std::string::npos);
    CHECK(saved.find("[providers.") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects the legacy providers namespace") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{"
        "\"providers\":{\"legacy\":{\"provider\":\"ollama\"}}},"
        "\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("model_providers") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config writes a provider api_key environment reference") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"my-openai\":{\"type\":\"openai\",\"api_key\":\"$OPENAI_API_KEY\"}},"
        "\"model\":{\"provider\":\"my-openai\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.my-openai]") != std::string::npos);
    CHECK(saved.find("api_key = \"$OPENAI_API_KEY\"") != std::string::npos);
}

TEST_CASE("config_web: GET /api/config reports only provider credential state") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"env-openai\":{\"type\":\"openai\","
               "\"api_key\":\"$OPENAI_API_KEY\"}},"
               "\"model\":{\"provider\":\"env-openai\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("OPENAI_API_KEY") == std::string::npos);
    CHECK(resp.body.find("\"token_env\"") == std::string::npos);
    CHECK(resp.body.find("\"has_api_key\":true") != std::string::npos);
}

TEST_CASE("config_web: redacted agent CLI provider credentials survive reads and unrelated saves") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"env-openai\":{\"type\":\"openai\",\"has_api_key\":true}},"
               "\"tts_providers\":{\"fish\":{\"type\":\"fish-audio\",\"has_api_key\":true}},"
               "\"stt_providers\":{\"tencent\":{\"type\":\"tencent-asr\",\"app_id\":\"123\","
               "\"has_secret_id\":true,\"has_secret_key\":true}},"
               "\"model\":{\"provider\":\"env-openai\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"tts\":{\"provider\":\"fish\",\"speed\":1},"
               "\"stt\":{\"provider\":\"tencent\",\"language\":\"zh\"},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[model_providers.env-openai]\n"
               "type = \"openai\"\n"
               "api_key = \"$MODEL_KEY\"\n\n"
               "[tts_providers.fish]\n"
               "type = \"fish-audio\"\n"
               "api_key = \"$TTS_KEY\"\n\n"
               "[stt_providers.tencent]\n"
               "type = \"tencent-asr\"\n"
               "app_id = \"123\"\n"
               "secret_id = \"stored-id\"\n"
               "secret_key = \"stored-key\"\n");

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(get_resp.status == 200);
    CHECK(get_resp.body.find("$MODEL_KEY") == std::string::npos);
    CHECK(get_resp.body.find("$TTS_KEY") == std::string::npos);
    CHECK(get_resp.body.find("stored-id") == std::string::npos);
    CHECK(get_resp.body.find("\"has_api_key\":true") != std::string::npos);
    CHECK(get_resp.body.find("\"has_secret_id\":true") != std::string::npos);
    CHECK(get_resp.body.find("\"has_secret_key\":true") != std::string::npos);

    const std::string body =
        "{\"config\":{\"hid\":{\"keyboard_layout\":\"azerty\","
        "\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
    HttpResponse post_resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(post_resp.status == 200);
    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"$MODEL_KEY\"") != std::string::npos);
    CHECK(saved.find("api_key = \"$TTS_KEY\"") != std::string::npos);
    CHECK(saved.find("secret_id = \"stored-id\"") != std::string::npos);
    CHECK(saved.find("secret_key = \"stored-key\"") != std::string::npos);
}

TEST_CASE("config_web: mixed flat voice credentials migrate without leaking") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"voice\":{\"type\":\"minimax-cn\",\"has_api_key\":true}},"
               "\"stt_providers\":{\"speech\":{\"type\":\"tencent-asr\","
               "\"has_api_key\":true,\"has_secret_id\":true,\"has_secret_key\":true}},"
               "\"model\":{\"provider\":\"openai\",\"model\":\"gpt-4o\"},"
               "\"tts\":{\"provider\":\"voice\",\"speed\":1},"
               "\"stt\":{\"provider\":\"speech\",\"language\":\"zh\"},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[tts_providers.voice]\n"
               "type = \"minimax-cn\"\n"
               "api_key = \"record-tts-key\"\n\n"
               "[stt_providers.speech]\n"
               "type = \"tencent-asr\"\n"
               "api_key = \"record-stt-key\"\n"
               "secret_id = \"record-secret-id\"\n"
               "secret_key = \"record-secret-key\"\n\n"
               "[tts]\n"
               "provider = \"voice\"\n"
               "api_key = \"flat-tts-key\"\n\n"
               "[stt]\n"
               "provider = \"speech\"\n"
               "api_key = \"flat-stt-key\"\n"
               "secret_id = \"\"\n"
               "secret_key = \"flat-secret-key\"\n");

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(get_resp.status == 200);
    CHECK(get_resp.body.find("flat-tts-key") == std::string::npos);
    CHECK(get_resp.body.find("flat-stt-key") == std::string::npos);
    CHECK(get_resp.body.find("flat-secret-key") == std::string::npos);
    cJSON* get_root = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(get_root != nullptr);
    cJSON* get_config = cJSON_GetObjectItem(get_root, "config");
    REQUIRE(get_config != nullptr);
    cJSON* get_tts = cJSON_GetObjectItem(get_config, "tts");
    cJSON* get_stt = cJSON_GetObjectItem(get_config, "stt");
    REQUIRE(get_tts != nullptr);
    REQUIRE(get_stt != nullptr);
    CHECK(cJSON_GetObjectItem(get_tts, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(get_stt, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(get_stt, "secret_id") == nullptr);
    CHECK(cJSON_GetObjectItem(get_stt, "secret_key") == nullptr);
    cJSON_Delete(get_root);

    const std::string body =
        "{\"config\":{\"hid\":{\"keyboard_layout\":\"azerty\","
        "\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
    HttpResponse post_resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(post_resp.status == 200);
    CHECK(post_resp.body.find("flat-tts-key") == std::string::npos);
    CHECK(post_resp.body.find("flat-stt-key") == std::string::npos);
    CHECK(post_resp.body.find("flat-secret-key") == std::string::npos);
    cJSON* post_root = cJSON_Parse(post_resp.body.c_str());
    REQUIRE(post_root != nullptr);
    cJSON* post_config = cJSON_GetObjectItem(post_root, "config");
    REQUIRE(post_config != nullptr);
    cJSON* post_tts = cJSON_GetObjectItem(post_config, "tts");
    cJSON* post_stt = cJSON_GetObjectItem(post_config, "stt");
    REQUIRE(post_tts != nullptr);
    REQUIRE(post_stt != nullptr);
    CHECK(cJSON_GetObjectItem(post_tts, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(post_stt, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(post_stt, "secret_id") == nullptr);
    CHECK(cJSON_GetObjectItem(post_stt, "secret_key") == nullptr);
    cJSON_Delete(post_root);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"flat-tts-key\"") != std::string::npos);
    CHECK(saved.find("api_key = \"flat-stt-key\"") != std::string::npos);
    CHECK(saved.find("secret_id = \"record-secret-id\"") != std::string::npos);
    CHECK(saved.find("secret_key = \"flat-secret-key\"") != std::string::npos);
    CHECK(saved.find("record-tts-key") == std::string::npos);
    CHECK(saved.find("record-stt-key") == std::string::npos);
    CHECK(saved.find("record-secret-key") == std::string::npos);

    const size_t tts_at = saved.find("[tts]\n");
    REQUIRE(tts_at != std::string::npos);
    const size_t tts_end = saved.find("\n[", tts_at + 1);
    CHECK(saved.substr(tts_at, tts_end - tts_at).find("api_key") == std::string::npos);
    const size_t stt_at = saved.find("[stt]\n");
    REQUIRE(stt_at != std::string::npos);
    const size_t stt_end = saved.find("\n[", stt_at + 1);
    const std::string stt_section = saved.substr(stt_at, stt_end - stt_at);
    CHECK(stt_section.find("api_key") == std::string::npos);
    CHECK(stt_section.find("secret_id") == std::string::npos);
    CHECK(stt_section.find("secret_key") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config renames a provider with its model reference") {
    // Renaming in the dialog posts the new providers map plus the rewritten
    // model sections in one request. The old section must be gone and the model
    // must point at the new name, or the reference resolves to nothing.
    StubEnv env;
    auto handle = start_server(env);

    const std::string create =
        "{\"config\":{\"model_providers\":{"
        "\"openai\":{\"type\":\"openai\",\"api_key\":\"sk-plain-secret-1234\"}},"
        "\"model\":{\"provider\":\"openai\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    CHECK(http_request(handle->port, "POST", "/api/config", create).status == 200);

    const std::string rename =
        "{\"config\":{\"model_providers\":{"
        "\"openai\":null,\"openai-work\":{\"type\":\"openai\"}},"
        "\"_provider_renames\":{\"model_providers\":{\"openai-work\":\"openai\"}},"
        "\"model\":{\"provider\":\"openai-work\",\"model\":\"x\"}},\"apply_wifi\":false}";
    CHECK(http_request(handle->port, "POST", "/api/config", rename).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.openai-work]") != std::string::npos);
    CHECK(saved.find("[model_providers.openai]\n") == std::string::npos);
    CHECK(saved.find("provider = \"openai-work\"") != std::string::npos);
    // The key rides along instead of being re-entered.
    CHECK(saved.find("api_key = \"sk-plain-secret-1234\"") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config drops named provider base_url for types that pin their endpoint") {
    // A [model_providers.*] base_url is inherited by any model referencing it
    // (applyProviderRef), and the runtime then clears it for types that pin
    // their endpoint. Storing one is dead config, so the metadata-backed
    // capability rule also has to apply while saving named providers.
    const char* types[] = {"openrouter", "kimi", "kimi-cn", "volcengine", "fake"};

    for (const char* type : types) {
        StubEnv env;
        auto handle = start_server(env);

        const std::string base_url = "https://gateway.example.com/v1";
        const std::string body =
            "{\"config\":{\"model_providers\":{\"named-" + std::string(type) + "\":{\"type\":\"" +
            type + "\",\"api_key\":\"k\",\"base_url\":\"" + base_url + "\"}},"
            "\"hid\":{\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        REQUIRE_MESSAGE(resp.status == 200, type);

        const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
        CHECK_MESSAGE(saved.find(std::string("[model_providers.named-") + type + "]") != std::string::npos,
                      type);
        CHECK_MESSAGE(saved.find(base_url) == std::string::npos, type);
    }
}

TEST_CASE("config_web: POST /api/config keeps named provider base_url for types that allow it") {
    struct Case {
        const char* type;
        const char* base_url;
    };
    const Case cases[] = {
        {"openai", "https://gateway.example.com/v1"},
        {"ollama", "http://127.0.0.1:11434"},
    };

    for (const Case& c : cases) {
        StubEnv env;
        auto handle = start_server(env);

        const std::string body =
            "{\"config\":{\"model_providers\":{\"named-" + std::string(c.type) + "\":{\"type\":\"" +
            c.type + "\",\"api_key\":\"k\",\"base_url\":\"" + c.base_url + "\"}},"
            "\"hid\":{\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        REQUIRE_MESSAGE(resp.status == 200, c.type);

        const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
        CHECK_MESSAGE(saved.find(std::string("base_url = \"") + c.base_url + "\"") != std::string::npos,
                      c.type);
    }
}

TEST_CASE("config_web: POST /api/config derives named provider base_url support from metadata") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string metadata_path = tmp + "/config-meta.json";
    write_file(metadata_path,
        "{\"sections\":[{\"name\":\"model_providers\",\"fields\":["
        "{\"key\":\"type\",\"widget\":\"select\"},"
        "{\"key\":\"api_key\",\"widget\":\"text\"},"
        "{\"key\":\"base_url\",\"widget\":\"text\",\"visibleWhen\":{\"all\":["
        "{\"field\":\"model_providers.type\",\"op\":\"in\","
        "\"values\":[\"custom-gateway\"]}]}}]}]}\n");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", metadata_path);
    auto handle = start_server(env);

    const std::string custom_base_url = "https://custom.example.com/v1";
    const std::string pinned_base_url = "https://pinned.example.com/v1";
    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"custom\":{\"type\":\"custom-gateway\",\"api_key\":\"k\",\"base_url\":\"" +
        custom_base_url + "\"},"
        "\"pinned\":{\"type\":\"pinned-provider\",\"api_key\":\"k\",\"base_url\":\"" +
        pinned_base_url + "\"}}},\"apply_wifi\":false}";
    const HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find(std::string("base_url = \"") + custom_base_url + "\"") != std::string::npos);
    CHECK(saved.find(pinned_base_url) == std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects ambiguous provider base_url metadata") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string metadata_path = tmp + "/config-meta.json";
    write_file(metadata_path,
        "{\"sections\":[{\"name\":\"model_providers\",\"fields\":["
        "{\"key\":\"type\",\"widget\":\"select\"},"
        "{\"key\":\"base_url\",\"widget\":\"text\",\"visibleWhen\":{"
        "\"all\":[{\"field\":\"model_providers.type\",\"op\":\"in\","
        "\"values\":[\"custom-gateway\"]}],"
        "\"any\":[{\"field\":\"model_providers.type\",\"op\":\"in\","
        "\"values\":[\"other-gateway\"]}]}}]}]}\n");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", metadata_path);
    auto handle = start_server(env);

    const std::string base_url = "https://custom.example.com/v1";
    const std::string body =
        "{\"config\":{\"model_providers\":{\"custom\":{"
        "\"type\":\"custom-gateway\",\"base_url\":\"" + base_url +
        "\"}}},\"apply_wifi\":false}";
    REQUIRE(http_request(handle->port, "POST", "/api/config", body).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find(base_url) == std::string::npos);
}

TEST_CASE("config_web: GET /api/config returns providers from the resolved config") {
    // Read path regression: config_to_json() serializes providers from the
    // AgentToml struct, but that struct is populated from `agent config
    // --format=json`. The Go webConfigDTO originally had no top-level
    // "providers" field, so the resolved JSON omitted it entirely and the UI
    // always rendered "No providers configured yet" no matter what agent.toml
    // held. This exercises the read path only -- it must not depend on a
    // preceding POST.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("\"stub-openai\"") != std::string::npos);
    CHECK(resp.body.find("\"stub-ollama\"") != std::string::npos);
    CHECK(resp.body.find("\"model_providers\":") != std::string::npos);
    CHECK(resp.body.find("\"providers\":") == std::string::npos);
    CHECK(resp.body.find("\"stub-openai\":{\"type\":\"openai\"") != std::string::npos);
    CHECK(resp.body.find("\"base_url\":\"http://127.0.0.1:11434\"") != std::string::npos);
    // Credentials are write-only on the read path. The UI gets only state.
    CHECK(resp.body.find("sk-stub-secret-1234") == std::string::npos);
    CHECK(resp.body.find("sk-s***1234") == std::string::npos);

    cJSON* root = cJSON_Parse(resp.body.c_str());
    REQUIRE(root != nullptr);
    cJSON* config = cJSON_GetObjectItem(root, "config");
    REQUIRE(config != nullptr);
    cJSON* model = cJSON_GetObjectItem(config, "model");
    REQUIRE(model != nullptr);
    CHECK(cJSON_GetObjectItem(model, "api_key") == nullptr);
    cJSON* providers = cJSON_GetObjectItem(config, "model_providers");
    REQUIRE(providers != nullptr);
    cJSON* openai = cJSON_GetObjectItem(providers, "stub-openai");
    REQUIRE(openai != nullptr);
    CHECK(cJSON_GetObjectItem(openai, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(openai, "token_env") == nullptr);
    cJSON* has_api_key = cJSON_GetObjectItem(openai, "has_api_key");
    REQUIRE(has_api_key != nullptr);
    CHECK((has_api_key->type & 0xff) == cJSON_True);
    cJSON_Delete(root);
}

TEST_CASE("config_web: POST /api/config preserves providers when saving another section") {
    // Regression test: handle_post_config() pre-loads the current config via
    // the agent CLI and applies the request body as a patch onto it. When the
    // resolved JSON omitted providers, every save of an unrelated section
    // started from an empty provider map and silently erased all of them,
    // leaving model.provider pointing at a provider that no longer existed.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"hid\":{\"keyboard_layout\":\"azerty\",\"pointer_mode\":\"absolute\"}},"
        "\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("keyboard_layout = \"azerty\"") != std::string::npos);
    CHECK(saved.find("[model_providers.stub-openai]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-stub-secret-1234\"") != std::string::npos);
    CHECK(saved.find("[model_providers.stub-ollama]") != std::string::npos);

    const size_t model_at = saved.find("[model]\n");
    REQUIRE(model_at != std::string::npos);
    const size_t model_end = saved.find("\n[", model_at + 1);
    const std::string model_section = saved.substr(model_at, model_end - model_at);
    CHECK(model_section.find("api_key") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config migrates a legacy model api_key to a provider record") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model\":{\"provider\":\"openai\",\"api_key\":\"sk-legacy-secret\","
               "\"model\":\"gpt-4o\",\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"api_key\":\"sk-legacy-secret\"},"
        "\"hid\":{\"keyboard_layout\":\"azerty\","
        "\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.openai]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-legacy-secret\"") != std::string::npos);

    const size_t model_at = saved.find("[model]\n");
    REQUIRE(model_at != std::string::npos);
    const size_t model_end = saved.find("\n[", model_at + 1);
    const std::string model_section = saved.substr(model_at, model_end - model_at);
    CHECK(model_section.find("api_key") == std::string::npos);
}

TEST_CASE("config_web: switching model providers never reassigns the resolved api_key") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model_providers\":{"
               "\"work-openai\":{\"type\":\"openai\",\"api_key\":\"sk-work-secret-aaaa\"},"
               "\"personal-openai\":{\"type\":\"openai\",\"api_key\":\"sk-personal-secret-bbbb\"}},"
               "\"model\":{\"provider\":\"work-openai\",\"api_key\":\"sk-work-secret-aaaa\","
               "\"model\":\"gpt-4o\",\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    // This is the same shape used when deleting the selected provider: the
    // provider map is authoritative and [model] switches to the replacement.
    // The loaded model.api_key is the resolved key for work-openai and must not
    // be attributed to personal-openai.
    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"personal-openai\":{\"type\":\"openai\"}},"
        "\"model\":{\"provider\":\"personal-openai\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.work-openai]") == std::string::npos);
    CHECK(saved.find("[model_providers.personal-openai]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-personal-secret-bbbb\"") != std::string::npos);
    CHECK(saved.find("sk-work-secret-aaaa") == std::string::npos);

    const size_t model_at = saved.find("[model]\n");
    REQUIRE(model_at != std::string::npos);
    const size_t model_end = saved.find("\n[", model_at + 1);
    const std::string model_section = saved.substr(model_at, model_end - model_at);
    CHECK(model_section.find("provider = \"personal-openai\"") != std::string::npos);
    CHECK(model_section.find("api_key") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config keeps the stored provider api_key when masked") {
    // The read path hands the UI a masked key ("sk-s***1234"); posting it back
    // unchanged must keep the stored secret. The masked-key branch originally
    // looked the name up in the map it had just cleared, so it always missed
    // and wrote an empty api_key, destroying the real key.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"stub-openai\":{\"type\":\"openai\",\"api_key\":\"sk-s***1234\"}},"
        "\"hid\":{\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"sk-stub-secret-1234\"") != std::string::npos);
    CHECK(saved.find("sk-s***1234") == std::string::npos);
    // Omitting stub-ollama from the payload deletes it: the posted map is
    // authoritative for which providers exist.
    CHECK(saved.find("[model_providers.stub-ollama]") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config keeps stored provider credentials when empty") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model_providers\":{"
               "\"literal\":{\"type\":\"openai\",\"api_key\":\"sk-literal-secret\"},"
               "\"from-env\":{\"type\":\"openai\",\"api_key\":\"$OPENAI_API_KEY\"}},"
               "\"model\":{\"provider\":\"literal\",\"model\":\"gpt-4o\"},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model_providers\":{"
        "\"literal\":{\"type\":\"openai\",\"api_key\":\"\"},"
        "\"from-env\":{\"type\":\"openai\",\"api_key\":\"\"}}},"
        "\"apply_wifi\":false}";
    REQUIRE(http_request(handle->port, "POST", "/api/config", body).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"sk-literal-secret\"") != std::string::npos);
    CHECK(saved.find("api_key = \"$OPENAI_API_KEY\"") != std::string::npos);
}

TEST_CASE("config_web: provider renames preserve every omitted secret") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"model-old\":{\"type\":\"openai\","
               "\"has_api_key\":true}},"
               "\"tts_providers\":{\"tts-old\":{\"type\":\"fish-audio\","
               "\"has_api_key\":true}},"
               "\"stt_providers\":{\"stt-old\":{\"type\":\"tencent-asr\","
               "\"has_api_key\":true,\"app_id\":\"1234\","
               "\"has_secret_id\":true,\"has_secret_key\":true,\"region\":\"ap-shanghai\"}},"
               "\"model\":{\"provider\":\"model-old\",\"model\":\"gpt-4o\"},"
               "\"tts\":{\"provider\":\"tts-old\",\"speed\":1},"
               "\"stt\":{\"provider\":\"stt-old\",\"language\":\"zh\"},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[model_providers.model-old]\ntype = \"openai\"\napi_key = \"sk-model-secret-1234\"\n\n"
               "[tts_providers.tts-old]\ntype = \"fish-audio\"\napi_key = \"sk-tts-secret-1234\"\n\n"
               "[stt_providers.stt-old]\ntype = \"tencent-asr\"\napi_key = \"sk-stt-secret-1234\"\n"
               "app_id = \"1234\"\nsecret_id = \"AKID-secret-1234\"\nsecret_key = \"secret-key-1234\"\nregion = \"ap-shanghai\"\n\n"
               "[model]\nprovider = \"model-old\"\nmodel = \"gpt-4o\"\n\n[tts]\nprovider = \"tts-old\"\n\n[stt]\nprovider = \"stt-old\"\n");

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(get_resp.status == 200);
    cJSON* root = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(root != nullptr);
    cJSON* config = cJSON_GetObjectItem(root, "config");
    REQUIRE(config != nullptr);
    cJSON* tts_records = cJSON_GetObjectItem(config, "tts_providers");
    cJSON* stt_records = cJSON_GetObjectItem(config, "stt_providers");
    REQUIRE(tts_records != nullptr);
    REQUIRE(stt_records != nullptr);
    cJSON* tts_record = cJSON_GetObjectItem(tts_records, "tts-old");
    cJSON* stt_record = cJSON_GetObjectItem(stt_records, "stt-old");
    REQUIRE(tts_record != nullptr);
    REQUIRE(stt_record != nullptr);
    CHECK(cJSON_GetObjectItem(tts_record, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(stt_record, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(stt_record, "secret_id") == nullptr);
    CHECK(cJSON_GetObjectItem(stt_record, "secret_key") == nullptr);
    REQUIRE(cJSON_GetObjectItem(tts_record, "has_api_key") != nullptr);
    REQUIRE(cJSON_GetObjectItem(stt_record, "has_api_key") != nullptr);
    REQUIRE(cJSON_GetObjectItem(stt_record, "has_secret_id") != nullptr);
    REQUIRE(cJSON_GetObjectItem(stt_record, "has_secret_key") != nullptr);
    CHECK((cJSON_GetObjectItem(tts_record, "has_api_key")->type & 0xff) == cJSON_True);
    CHECK((cJSON_GetObjectItem(stt_record, "has_api_key")->type & 0xff) == cJSON_True);
    CHECK((cJSON_GetObjectItem(stt_record, "has_secret_id")->type & 0xff) == cJSON_True);
    CHECK((cJSON_GetObjectItem(stt_record, "has_secret_key")->type & 0xff) == cJSON_True);
    cJSON_Delete(root);

    const std::string body =
        "{\"config\":{"
        "\"model_providers\":{\"model-old\":null,\"model-new\":{\"type\":\"openai\"}},"
        "\"tts_providers\":{\"tts-old\":null,\"tts-new\":{\"type\":\"fish-audio\"}},"
        "\"stt_providers\":{\"stt-old\":null,\"stt-new\":{\"type\":\"tencent-asr\","
        "\"app_id\":\"1234\",\"region\":\"ap-shanghai\"}},"
        "\"_provider_renames\":{"
        "\"model_providers\":{\"model-new\":\"model-old\"},"
        "\"tts_providers\":{\"tts-new\":\"tts-old\"},"
        "\"stt_providers\":{\"stt-new\":\"stt-old\"}},"
        "\"model\":{\"provider\":\"model-new\",\"model\":\"gpt-4o\"},"
        "\"tts\":{\"provider\":\"tts-new\",\"speed\":1},"
        "\"stt\":{\"provider\":\"stt-new\",\"language\":\"zh\"}},"
        "\"apply_wifi\":false}";
    REQUIRE(http_request(handle->port, "POST", "/api/config", body).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[model_providers.model-new]") != std::string::npos);
    CHECK(saved.find("[tts_providers.tts-new]") != std::string::npos);
    CHECK(saved.find("[stt_providers.stt-new]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-model-secret-1234\"") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-tts-secret-1234\"") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-stt-secret-1234\"") != std::string::npos);
    CHECK(saved.find("secret_id = \"AKID-secret-1234\"") != std::string::npos);
    CHECK(saved.find("secret_key = \"secret-key-1234\"") != std::string::npos);
    CHECK(saved.find("_provider_renames") == std::string::npos);
    CHECK(saved.find("model-old") == std::string::npos);
    CHECK(saved.find("tts-old") == std::string::npos);
    CHECK(saved.find("stt-old") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config keeps a submitted api_key that contains the mask marker") {
    // Only the exact mask for that provider means "unchanged". Treating any
    // value containing "***" as the mask dropped a real key that happens to
    // contain it, and for a provider name absent from the previous config it
    // wrote an empty key while still answering 200.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model_providers\":{"
        // Same provider as the stored one, but not its mask: must be written.
        "\"stub-openai\":{\"type\":\"openai\",\"api_key\":\"sk-a***b-real-key\"},"
        // Provider the previous config never had: the submitted key must survive.
        "\"brand-new\":{\"type\":\"openai\",\"api_key\":\"sk-n***w-other-key\"}},"
        "\"hid\":{\"pointer_mode\":\"absolute\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"sk-a***b-real-key\"") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-n***w-other-key\"") != std::string::npos);
    CHECK(saved.find("sk-stub-secret-1234") == std::string::npos);
}

// The provider select leads with an empty placeholder so the add shortcut can
// never become the value a fresh config falls back to. That makes an empty
// model.provider reachable from the UI for the first time, so the backend has to
// reject it with a message naming the field rather than writing a config the
// agent would refuse to start on.
TEST_CASE("config_web: POST /api/config rejects an empty model provider") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string before = read_file(handle->tmp_dir + "/agent.toml");

    const std::string body =
        "{\"config\":{\"model_providers\":{},"
        "\"model\":{\"provider\":\"\",\"model\":\"x\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("model.provider") != std::string::npos);

    // A rejected save must leave the on-disk config untouched.
    CHECK(read_file(handle->tmp_dir + "/agent.toml") == before);
}

TEST_CASE("config_web: POST /api/config rejects a malformed model_providers section") {
    StubEnv env;
    auto handle = start_server(env);

    struct Case {
        const char* providers;
        const char* expected_fragment;
    };
    const Case cases[] = {
        {"\"not-an-object\"", "model_providers"},
        {"{\"x\":\"not-an-object\"}", "model_providers.x"},
        {"{\"bad name!\":{\"type\":\"openai\"}}", "bad name!"},
    };

    for (const Case& c : cases) {
        const std::string body =
            std::string("{\"config\":{\"model_providers\":") + c.providers +
            "},\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        CHECK_MESSAGE(resp.status == 400, c.providers);
        CHECK_MESSAGE(resp.body.find(c.expected_fragment) != std::string::npos, c.providers);
    }

    const std::string canonical_body =
        "{\"config\":{\"model_providers\":{\"bad.name\":{\"type\":\"openai\"}}},"
        "\"apply_wifi\":false}";
    HttpResponse canonical_resp =
        http_request(handle->port, "POST", "/api/config", canonical_body);
    CHECK(canonical_resp.status == 400);
    CHECK(canonical_resp.body.find("model_providers") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config writes keyboard layout and requires reboot") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"hid\":{\"keyboard_layout\":\"azerty\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
    CHECK(resp.body.find("\"message\":\"config saved; USB HID configuration changed; reboot required\"") != std::string::npos);
    CHECK(resp.body.find("\"agent_restart_scheduled\":false") != std::string::npos);
    CHECK(resp.body.find("\"usb_reenumeration_scheduled\":false") != std::string::npos);
    CHECK(resp.body.find("\"usbhid_restart_required\":true") != std::string::npos);
    CHECK(resp.body.find("\"reboot_required\":true") != std::string::npos);

    std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    CHECK(saved_buffer.str().find("keyboard_layout = \"azerty\"") != std::string::npos);
}

// default_platform was removed in favor of [device].device_type. The C++
// config parser silently ignores unknown top-level keys for forward
// compatibility, so old config files with default_platform still load.
// This test verifies that submitting it through the API has no effect and
// it does not get written back.
TEST_CASE("config_web: POST /api/config ignores the removed default_platform field") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"agent\":{\"default_platform\":\"android\"},"
        "\"search\":{\"provider\":\"duckduckgo\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("default_platform") == std::string::npos);
    CHECK(saved.find("device_type = \"iOS\"") != std::string::npos);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    CHECK(get_resp.status == 200);
    CHECK(get_resp.body.find("default_platform") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config same-pointer-mode device type change requires reboot") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"macOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
    CHECK(resp.body.find("\"message\":\"config saved; USB HID configuration changed; reboot required\"") != std::string::npos);
    CHECK(resp.body.find("\"agent_restart_scheduled\":false") != std::string::npos);
    CHECK(resp.body.find("\"usbhid_restart_required\":true") != std::string::npos);
    CHECK(resp.body.find("\"reboot_required\":true") != std::string::npos);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("device_type = \"macOS\"") != std::string::npos);
    CHECK(saved.find("pointer_mode =") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config omitting temperature clears a saved value") {
    // Regression test: update_model_from_json() applies JSON as a patch onto the
    // config pre-loaded from disk. Omitting the temperature key must clear the
    // previously-saved value (has_temperature=false), letting the UI unset it.
    StubEnv env;
    auto handle = start_server(env);

    // First save an explicit temperature.
    const std::string with_temp =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\","
        "\"temperature\":0.7},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse first = http_request(handle->port, "POST", "/api/config", with_temp);
    CHECK(first.status == 200);
    {
        std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
        REQUIRE(saved_in.good());
        std::ostringstream buf;
        buf << saved_in.rdbuf();
        CHECK(buf.str().find("temperature = 0.7") != std::string::npos);
    }

    // Now save again without the temperature key: it must be cleared.
    const std::string without_temp =
        "{\"config\":{\"model\":{\"temperature\":null},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse second = http_request(handle->port, "POST", "/api/config", without_temp);
    CHECK(second.status == 200);

    std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    const std::string saved = saved_buffer.str();
    CHECK(saved.find("temperature") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config preserves voice notification settings") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"voice_notifications\":{\"enabled\":false,\"max_pending\":6,"
        "\"response_tail\":{\"enabled\":false,\"max_items\":1,\"max_text_chars\":72},"
        "\"expiration\":{\"default_ttl_seconds\":120,\"code_ttl_seconds\":{\"network\":123}}},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    REQUIRE(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[voice_notifications]") != std::string::npos);
    CHECK(saved.find("default_locale") == std::string::npos);
    CHECK(saved.find("max_pending = 6") != std::string::npos);
    CHECK(saved.find("[voice_notifications.response_tail]") != std::string::npos);
    CHECK(saved.find("max_text_chars = 72") != std::string::npos);
    CHECK(saved.find("[voice_notifications.expiration]") != std::string::npos);
    CHECK(saved.find("default_ttl_seconds = 120") != std::string::npos);
    CHECK(saved.find("[voice_notifications.expiration.code_ttl_seconds]") != std::string::npos);
    CHECK(saved.find("network = 123") != std::string::npos);
    CHECK(saved.find("storage = 900") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects invalid voice notification integers and TTL keys") {
    StubEnv env;
    auto handle = start_server(env);

    struct InvalidCase {
        const char* name;
        const char* voice_notifications;
        const char* expected_error;
    };
    const InvalidCase cases[] = {
        {"enabled_type", "{\"enabled\":\"yes\"}", "expected bool"},
        {"response_tail_type", "{\"response_tail\":[]}", "expected object"},
        {"response_tail_enabled_type", "{\"response_tail\":{\"enabled\":\"yes\"}}", "expected bool"},
        {"expiration_type", "{\"expiration\":[]}", "expected object"},
        {"code_ttl_seconds_type", "{\"expiration\":{\"code_ttl_seconds\":[]}}", "expected object"},
        {"max_pending_negative", "{\"max_pending\":-1}", "non-negative integer"},
        {"max_pending", "{\"max_pending\":0.5}", "non-negative integer"},
        {"max_items", "{\"response_tail\":{\"max_items\":0.5}}", "non-negative integer"},
        {"max_text_chars", "{\"response_tail\":{\"max_text_chars\":0.5}}", "non-negative integer"},
        {"default_ttl_seconds", "{\"expiration\":{\"default_ttl_seconds\":0.5}}", "non-negative integer"},
        {"code_ttl_seconds", "{\"expiration\":{\"code_ttl_seconds\":{\"network\":0.5}}}", "non-negative integer"},
        {"invalid_ttl_key", "{\"expiration\":{\"code_ttl_seconds\":{\"bad key\":123}}}", "bare TOML key"},
    };

    for (const auto& test_case : cases) {
        const std::string body =
            std::string("{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},") +
            "\"voice_notifications\":" + test_case.voice_notifications + "," +
            "\"device\":{\"device_type\":\"iOS\"}," +
            "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        CHECK_MESSAGE(resp.status == 400, test_case.name << ": status=" << resp.status);
        CHECK_MESSAGE(resp.body.find(test_case.expected_error) != std::string::npos,
                      test_case.name << ": body=" << resp.body);
    }
}

TEST_CASE("config_web: POST /api/config writes ota section") {
    // Regression test: update_config_from_json() must handle the ota section.
    // It was added to AgentToml, TOML read/write, and validation, but was
    // missed here, so a saved github_proxy_url silently failed to persist.
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"ota\":{\"github_proxy_url\":\"https://gh-proxy.com\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse post_resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(post_resp.status == 200);

    std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    const std::string saved = saved_buffer.str();
    CHECK(saved.find("[ota]") != std::string::npos);
    CHECK(saved.find("github_proxy_url = \"https://gh-proxy.com\"") != std::string::npos);
}

TEST_CASE("config_web: GET /api/config returns ota and voice notification sections from resolved config") {
    // Regression test: config_to_json() must serialize the ota section from
    // the agent's resolved config. It was added to AgentToml and TOML
    // read/write, but was missed here, so github_proxy_url never came back
    // on GET even though it was correctly persisted to agent.toml.
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    CHECK(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* ota = cJSON_GetObjectItem(config, "ota");
    REQUIRE(ota != nullptr);
    cJSON* proxy_url = cJSON_GetObjectItem(ota, "github_proxy_url");
    REQUIRE(proxy_url != nullptr);
    REQUIRE(proxy_url->valuestring != nullptr);
    CHECK(std::string(proxy_url->valuestring) == "https://gh-proxy.com");
    cJSON* voice_notifications = cJSON_GetObjectItem(config, "voice_notifications");
    REQUIRE(voice_notifications != nullptr);
    CHECK(required_json_int(voice_notifications, "max_pending") == 6);
    cJSON* expiration = cJSON_GetObjectItem(voice_notifications, "expiration");
    REQUIRE(expiration != nullptr);
    CHECK(required_json_int(expiration, "default_ttl_seconds") == 120);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: POST /api/config accepts empty audio_archive storage_path") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"audio_archive\":{\"enabled\":true,\"max_files\":123,\"max_size_mb\":45,"
        "\"storage_path\":\"\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
}

TEST_CASE("config_web: POST /api/config writes custom_instruction") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},"
        "\"agent\":{\"custom_instruction\":\"Use custom behavior.\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("custom_instruction = \"Use custom behavior.\"") != std::string::npos);
    CHECK(saved.rfind("instruction =", 0) != 0);
    CHECK(saved.find("\ninstruction =") == std::string::npos);
}

TEST_CASE("config_web: POST /api/config ignores legacy instruction") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("duckduckgo", false));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},"
        "\"agent\":{\"custom_instruction\":\"\",\"instruction\":\"legacy value\"}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("custom_instruction") == std::string::npos);
    CHECK(saved.rfind("instruction =", 0) != 0);
    CHECK(saved.find("\ninstruction =") == std::string::npos);
}

TEST_CASE("config_web: GET /api/config preserves search api key presence from resolved config") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("brave", true));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* search = cJSON_GetObjectItem(config, "search");
    REQUIRE(search != nullptr);
    cJSON* provider = cJSON_GetObjectItem(search, "provider");
    REQUIRE(provider != nullptr);
    REQUIRE(provider->valuestring != nullptr);
    CHECK(std::string(provider->valuestring) == "brave");
    cJSON* has_api_key = cJSON_GetObjectItem(search, "has_api_key");
    REQUIRE(has_api_key != nullptr);
    CHECK((has_api_key->type & 0xff) == cJSON_True);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: POST /api/config preserves stored search api key when GET redacts it") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("brave", true));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
        "[search]\n"
        "provider = \"brave\"\n"
        "api_key = \"search-test-key\"\n");

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    char* config_text = cJSON_PrintUnformatted(config);
    REQUIRE(config_text != nullptr);
    std::string post_body = std::string("{\"config\":") + config_text + ",\"apply_wifi\":false}";
    free(config_text);
    cJSON_Delete(parsed);

    HttpResponse post_resp = http_request(handle->port, "POST", "/api/config", post_body);
    CHECK(post_resp.status == 200);
    std::ifstream saved_in((handle->tmp_dir + "/agent.toml").c_str());
    REQUIRE(saved_in.good());
    std::ostringstream saved_buffer;
    saved_buffer << saved_in.rdbuf();
    const std::string saved = saved_buffer.str();
    CHECK(saved.find("api_key = \"search-test-key\"") != std::string::npos);
}

TEST_CASE("config_web: config test accepts stored search api key when GET redacts it") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("brave", true));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
        "[search]\n"
        "provider = \"brave\"\n"
        "api_key = \"search-test-key\"\n");

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* search = cJSON_GetObjectItem(config, "search");
    REQUIRE(search != nullptr);
    char* search_text = cJSON_PrintUnformatted(search);
    REQUIRE(search_text != nullptr);
    std::string test_body = std::string("{\"section\":\"search\",\"values\":") + search_text + "}";
    free(search_text);
    cJSON_Delete(parsed);

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":true") != std::string::npos);
}

TEST_CASE("config_web: config test accepts redacted search api key from wire payload") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("brave", true));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* search = cJSON_GetObjectItem(config, "search");
    REQUIRE(search != nullptr);
    char* search_text = cJSON_PrintUnformatted(search);
    REQUIRE(search_text != nullptr);
    std::string test_body = std::string("{\"section\":\"search\",\"values\":") + search_text + "}";
    free(search_text);
    cJSON_Delete(parsed);

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":true") != std::string::npos);
}

TEST_CASE("config_web: config test rejects blank search api key without stored marker") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", resolved_config_json("brave", false));
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* search = cJSON_GetObjectItem(config, "search");
    REQUIRE(search != nullptr);
    char* search_text = cJSON_PrintUnformatted(search);
    REQUIRE(search_text != nullptr);
    std::string test_body = std::string("{\"section\":\"search\",\"values\":") + search_text + "}";
    free(search_text);
    cJSON_Delete(parsed);

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":false") != std::string::npos);
    CHECK(test_resp.body.find("required for brave") != std::string::npos);
}

TEST_CASE("config_web: device config test derives pointer mode") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string ios_body =
        "{\"section\":\"device\",\"values\":{"
        "\"device_type\":\"iOS\""
        "}}";

    HttpResponse ios_resp = http_request(handle->port, "POST", "/api/config/test", ios_body);
    REQUIRE(ios_resp.status == 200);
    cJSON* ios_json = cJSON_Parse(ios_resp.body.c_str());
    REQUIRE(ios_json != nullptr);
    cJSON* ios_ok = cJSON_GetObjectItem(ios_json, "ok");
    REQUIRE(ios_ok != nullptr);
    CHECK((ios_ok->type & 0xff) == cJSON_True);
    cJSON* ios_type = required_test_result(ios_json, "device_type");
    REQUIRE(ios_type != nullptr);
    cJSON* ios_type_passed = cJSON_GetObjectItem(ios_type, "passed");
    REQUIRE(ios_type_passed != nullptr);
    CHECK((ios_type_passed->type & 0xff) == cJSON_True);
    CHECK(required_json_string(ios_type, "detail") == "effective pointer_mode: absolute");
    cJSON_Delete(ios_json);

    const std::string android_body =
        "{\"section\":\"device\",\"values\":{"
        "\"device_type\":\"Android\""
        "}}";

    HttpResponse android_resp = http_request(handle->port, "POST", "/api/config/test", android_body);
    REQUIRE(android_resp.status == 200);
    cJSON* android_json = cJSON_Parse(android_resp.body.c_str());
    REQUIRE(android_json != nullptr);
    cJSON* android_ok = cJSON_GetObjectItem(android_json, "ok");
    REQUIRE(android_ok != nullptr);
    CHECK((android_ok->type & 0xff) == cJSON_True);
    cJSON* android_type = required_test_result(android_json, "device_type");
    REQUIRE(android_type != nullptr);
    cJSON* android_type_passed = cJSON_GetObjectItem(android_type, "passed");
    REQUIRE(android_type_passed != nullptr);
    CHECK((android_type_passed->type & 0xff) == cJSON_True);
    CHECK(required_json_string(android_type, "detail") == "effective pointer_mode: touchscreen");
    cJSON_Delete(android_json);

    const std::string invalid_body =
        "{\"section\":\"device\",\"values\":{"
        "\"device_type\":\"blackberry\""
        "}}";

    HttpResponse invalid_resp = http_request(handle->port, "POST", "/api/config/test", invalid_body);
    REQUIRE(invalid_resp.status == 200);
    cJSON* invalid_json = cJSON_Parse(invalid_resp.body.c_str());
    REQUIRE(invalid_json != nullptr);
    cJSON* invalid_ok = cJSON_GetObjectItem(invalid_json, "ok");
    REQUIRE(invalid_ok != nullptr);
    CHECK((invalid_ok->type & 0xff) == cJSON_False);
    cJSON* invalid_type = required_test_result(invalid_json, "device_type");
    REQUIRE(invalid_type != nullptr);
    cJSON* invalid_type_passed = cJSON_GetObjectItem(invalid_type, "passed");
    REQUIRE(invalid_type_passed != nullptr);
    CHECK((invalid_type_passed->type & 0xff) == cJSON_False);
    CHECK(required_json_string(invalid_type, "detail") == "must be iOS, Android, macOS, windows, or linux");
    cJSON_Delete(invalid_json);

    const std::string missing_body =
        "{\"section\":\"device\",\"values\":{"
        "}}";

    HttpResponse missing_resp = http_request(handle->port, "POST", "/api/config/test", missing_body);
    REQUIRE(missing_resp.status == 200);
    cJSON* missing_json = cJSON_Parse(missing_resp.body.c_str());
    REQUIRE(missing_json != nullptr);
    cJSON* missing_ok = cJSON_GetObjectItem(missing_json, "ok");
    REQUIRE(missing_ok != nullptr);
    CHECK((missing_ok->type & 0xff) == cJSON_True);
    cJSON* missing_type = required_test_result(missing_json, "device_type");
    REQUIRE(missing_type != nullptr);
    cJSON* missing_type_passed = cJSON_GetObjectItem(missing_type, "passed");
    REQUIRE(missing_type_passed != nullptr);
    CHECK((missing_type_passed->type & 0xff) == cJSON_True);
    CHECK(required_json_string(missing_type, "detail") == "effective pointer_mode: absolute");
    cJSON_Delete(missing_json);

    const std::vector<std::string> non_string_bodies = {
        "{\"section\":\"device\",\"values\":{\"device_type\":null}}",
        "{\"section\":\"device\",\"values\":{\"device_type\":123}}",
        "{\"section\":\"device\",\"values\":{\"device_type\":{}}}"
    };

    for (size_t i = 0; i < non_string_bodies.size(); ++i) {
        HttpResponse non_string_resp = http_request(handle->port, "POST", "/api/config/test", non_string_bodies[i]);
        REQUIRE(non_string_resp.status == 200);
        cJSON* non_string_json = cJSON_Parse(non_string_resp.body.c_str());
        REQUIRE(non_string_json != nullptr);
        cJSON* non_string_ok = cJSON_GetObjectItem(non_string_json, "ok");
        REQUIRE(non_string_ok != nullptr);
        CHECK((non_string_ok->type & 0xff) == cJSON_False);
        cJSON* non_string_type = required_test_result(non_string_json, "device_type");
        REQUIRE(non_string_type != nullptr);
        cJSON* non_string_type_passed = cJSON_GetObjectItem(non_string_type, "passed");
        REQUIRE(non_string_type_passed != nullptr);
        CHECK((non_string_type_passed->type & 0xff) == cJSON_False);
        CHECK(required_json_string(non_string_type, "detail") == "must be a string");
        cJSON_Delete(non_string_json);
    }
}

TEST_CASE("config_web: hid config test requires extension keyboard device") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string missing_body =
        "{\"section\":\"hid\",\"values\":{"
        "\"keyboard_device\":\"/dev/null\","
        "\"mouse_device\":\"/dev/null\","
        "\"android_keyboard_device\":\"\""
        "}}";

    HttpResponse missing_resp = http_request(handle->port, "POST", "/api/config/test", missing_body);
    REQUIRE(missing_resp.status == 200);
    cJSON* missing_json = cJSON_Parse(missing_resp.body.c_str());
    REQUIRE(missing_json != nullptr);
    cJSON* missing_ok = cJSON_GetObjectItem(missing_json, "ok");
    REQUIRE(missing_ok != nullptr);
    CHECK((missing_ok->type & 0xff) == cJSON_False);
    cJSON* missing_android = required_test_result(missing_json, "android_keyboard_device");
    REQUIRE(missing_android != nullptr);
    cJSON* missing_android_passed = cJSON_GetObjectItem(missing_android, "passed");
    REQUIRE(missing_android_passed != nullptr);
    CHECK((missing_android_passed->type & 0xff) == cJSON_False);
    CHECK(required_json_string(missing_android, "detail") == "path is empty");
    cJSON_Delete(missing_json);

    const std::string valid_body =
        "{\"section\":\"hid\",\"values\":{"
        "\"keyboard_device\":\"/dev/null\","
        "\"mouse_device\":\"/dev/null\","
        "\"android_keyboard_device\":\"/dev/null\""
        "}}";

    HttpResponse valid_resp = http_request(handle->port, "POST", "/api/config/test", valid_body);
    REQUIRE(valid_resp.status == 200);
    cJSON* valid_json = cJSON_Parse(valid_resp.body.c_str());
    REQUIRE(valid_json != nullptr);
    cJSON* valid_ok = cJSON_GetObjectItem(valid_json, "ok");
    REQUIRE(valid_ok != nullptr);
    CHECK((valid_ok->type & 0xff) == cJSON_True);
    cJSON* valid_android = required_test_result(valid_json, "android_keyboard_device");
    REQUIRE(valid_android != nullptr);
    cJSON* valid_android_passed = cJSON_GetObjectItem(valid_android, "passed");
    REQUIRE(valid_android_passed != nullptr);
    CHECK((valid_android_passed->type & 0xff) == cJSON_True);
    CHECK(required_json_string(valid_android, "detail") == "/dev/null exists");
    cJSON_Delete(valid_json);

    const std::string not_found_body =
        "{\"section\":\"hid\",\"values\":{"
        "\"keyboard_device\":\"/dev/null\","
        "\"mouse_device\":\"/dev/null\","
        "\"android_keyboard_device\":\"/dev/definitely-missing-hidg9\""
        "}}";

    HttpResponse not_found_resp = http_request(handle->port, "POST", "/api/config/test", not_found_body);
    REQUIRE(not_found_resp.status == 200);
    cJSON* not_found_json = cJSON_Parse(not_found_resp.body.c_str());
    REQUIRE(not_found_json != nullptr);
    cJSON* not_found_ok = cJSON_GetObjectItem(not_found_json, "ok");
    REQUIRE(not_found_ok != nullptr);
    CHECK((not_found_ok->type & 0xff) == cJSON_False);
    cJSON* not_found_android = required_test_result(not_found_json, "android_keyboard_device");
    REQUIRE(not_found_android != nullptr);
    cJSON* not_found_android_passed = cJSON_GetObjectItem(not_found_android, "passed");
    REQUIRE(not_found_android_passed != nullptr);
    CHECK((not_found_android_passed->type & 0xff) == cJSON_False);
    CHECK(required_json_string(not_found_android, "detail") == "/dev/definitely-missing-hidg9 not found");
    cJSON_Delete(not_found_json);
}

TEST_CASE("config_web: config test rejects wakeup trigger for text input") {
    StubEnv env;
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* agent = cJSON_GetObjectItem(config, "agent");
    REQUIRE(agent != nullptr);
    cJSON_DeleteItemFromObject(agent, "input_mode");
    cJSON_AddStringToObject(agent, "input_mode", "text");
    cJSON_DeleteItemFromObject(agent, "trigger_mode");
    cJSON_AddStringToObject(agent, "trigger_mode", "wakeup");

    char* agent_text = cJSON_PrintUnformatted(agent);
    REQUIRE(agent_text != nullptr);
    std::string test_body = std::string("{\"section\":\"agent\",\"values\":") + agent_text + "}";
    free(agent_text);
    cJSON_Delete(parsed);

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":false") != std::string::npos);
    CHECK(test_resp.body.find("\"check\":\"trigger_mode\"") != std::string::npos);
    CHECK(test_resp.body.find("wakeup requires input_mode stt") != std::string::npos);
}

TEST_CASE("config_web: config test reports removed audio input mode hint") {
    StubEnv env;
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config");
    REQUIRE(get_resp.status == 200);
    cJSON* parsed = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* agent = cJSON_GetObjectItem(config, "agent");
    REQUIRE(agent != nullptr);
    cJSON_DeleteItemFromObject(agent, "input_mode");
    cJSON_AddStringToObject(agent, "input_mode", "audio");

    char* agent_text = cJSON_PrintUnformatted(agent);
    REQUIRE(agent_text != nullptr);
    std::string test_body = std::string("{\"section\":\"agent\",\"values\":") + agent_text + "}";
    free(agent_text);
    cJSON_Delete(parsed);

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":false") != std::string::npos);
    CHECK(test_resp.body.find("\"check\":\"input_mode\"") != std::string::npos);
    CHECK(test_resp.body.find("audio mode has been removed; use stt instead") != std::string::npos);
}

TEST_CASE("config_web: tts config test invokes the agent runtime") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string log_path = tmp + "/config-test.log";
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
    auto handle = start_server(env);

    const std::string test_body =
        "{\"section\":\"tts\",\"values\":{"
        "\"provider\":\"minimax-cn\","
        "\"api_key\":\"tts-test-key\","
        "\"model\":\"speech-2.8-hd\","
        "\"voice_id\":\"male-qn-qingse\","
        "\"emotion\":\"happy\","
        "\"speed\":1"
        "}}";

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":true") != std::string::npos);
    CHECK(test_resp.body.find("tts_playback") != std::string::npos);
    CHECK(test_resp.body.find("test passed") != std::string::npos);
    REQUIRE(wait_for_file_contains(log_path, "stdin:", 1000));
    const std::string log = read_file(log_path);
    CHECK(log.find("config-test") != std::string::npos);
    CHECK(log.find("--section=tts") != std::string::npos);
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
}

TEST_CASE("config_web: fish audio TTS test accepts empty reference id and agent logs") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string log_path = tmp + "/config-test.log";
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_STDERR", "[tts] fish-audio: connected");
    auto handle = start_server(env);

    const std::string test_body =
        "{\"section\":\"tts\",\"values\":{"
        "\"provider\":\"fish-audio\","
        "\"api_key\":\"tts-test-key\","
        "\"model\":\"s2-pro\","
        "\"reference_id\":\"\","
        "\"speed\":1"
        "}}";

    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    CHECK(test_resp.body.find("\"ok\":true") != std::string::npos);
    CHECK(test_resp.body.find("\"check\":\"tts_playback\"") != std::string::npos);
    REQUIRE(wait_for_file_contains(log_path, "\"reference_id\":\"\"", 1000));
    const std::string log = read_file(log_path);
    CHECK(log.find("\"provider\":\"fish-audio\"") != std::string::npos);
    CHECK(log.find("\"model\":\"s2-pro\"") != std::string::npos);
    CHECK(log.find("\"reference_id\":\"\"") != std::string::npos);
}

TEST_CASE("config_web: GET /api/models forwards the query string to the agent") {
    // Regression test: the request line parser strips the query off
    // HttpRequest::path so routes can compare it for equality. The /api/models
    // route then searched that already-stripped path for '?', always came up
    // empty, and proxied a bare /api/models -- which the agent rejects with
    // 400 "provider parameter is required", so the model picker could never
    // load any models.
    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    auto handle = start_server(env);

    HttpResponse resp =
        http_request(handle->port, "GET", "/api/models?provider=openai&locale=zh-CN", "");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("/api/models?provider=openai&locale=zh-CN") != std::string::npos);
}

TEST_CASE("config_web: stt live test proxies start and stop to agent") {
    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{"
        "\"provider\":\"tencent-asr\","
        "\"app_id\":\"app-1\","
        "\"secret_id\":\"sid\","
        "\"secret_key\":\"skey\","
        "\"region\":\"ap-shanghai\","
        "\"engine_model_type\":\"16k_zh\""
        "},"
        "\"audio_values\":{"
        "\"socket\":\"/tmp/audio.sock\","
        "\"sample_rate\":16000,"
        "\"channels\":1,"
        "\"bit_width\":16,"
        "\"playback_backend\":\"audio_service\""
        "}}";

    HttpResponse start_resp = http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);
    CHECK(start_resp.status == 200);
    CHECK(start_resp.body.find("\"status\":\"recording\"") != std::string::npos);

    HttpResponse stop_resp = http_request(handle->port, "POST", "/api/config/test/stt/stop", "{}");
    CHECK(stop_resp.status == 200);
    CHECK(stop_resp.body.find("agent live transcript") != std::string::npos);
    CHECK(stop_resp.body.find("streaming upload") != std::string::npos);

    const std::vector<CapturedHTTPRequest> requests = agent_server.requests();
    REQUIRE(requests.size() == 2);
    CHECK(requests[0].method == "POST");
    CHECK(requests[0].path == "/api/config-test/stt/start");
    CHECK(requests[0].body.find("\"provider\":\"tencent-asr\"") != std::string::npos);
    CHECK(requests[0].body.find("\"app_id\":\"app-1\"") != std::string::npos);
    CHECK(requests[0].body.find("\"socket\":\"/tmp/audio.sock\"") != std::string::npos);
    CHECK(requests[1].method == "POST");
    CHECK(requests[1].path == "/api/config-test/stt/stop");
}

TEST_CASE("config_web: stt live test waits for a scheduled agent restart") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string restart_script = tmp + "/restart-agent.sh";
    const std::string restart_log = tmp + "/restart.log";
    write_file(restart_script,
               "#!/bin/sh\n"
               "echo started >> \"$AIDEN_AGENT_RESTART_TEST_LOG\"\n"
               "sleep 0.35\n"
               "echo ready >> \"$AIDEN_AGENT_RESTART_TEST_LOG\"\n");
    REQUIRE(::chmod(restart_script.c_str(), 0755) == 0);

    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_INIT_SCRIPT", restart_script);
    env.set("AIDEN_AGENT_RESTART_TEST_LOG", restart_log);
    auto handle = start_server(env);

    REQUIRE(http_request(handle->port, "POST", "/api/system/env", "{\"system_env\":\"\"}").status == 200);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"openai-whisper\",\"api_key\":\"sk-live\",\"model\":\"whisper-1\"},"
        "\"audio_values\":{\"socket\":\"/tmp/audio.sock\",\"sample_rate\":16000,\"channels\":1,\"bit_width\":16}}";
    const auto started_at = std::chrono::steady_clock::now();
    HttpResponse start_resp =
        http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);
    const auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(
        std::chrono::steady_clock::now() - started_at);

    REQUIRE(start_resp.status == 200);
    CHECK(elapsed.count() >= 250);
    REQUIRE(wait_for_file_contains(restart_log, "ready", 1000));
    REQUIRE(agent_server.requests().size() == 1);
}

TEST_CASE("config_web: stt live test defers agent restart until recording stops") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string restart_script = tmp + "/restart-agent.sh";
    const std::string restart_log = tmp + "/restart.log";
    write_file(restart_script,
               "#!/bin/sh\n"
               "echo restarted >> \"$AIDEN_AGENT_RESTART_TEST_LOG\"\n");
    REQUIRE(::chmod(restart_script.c_str(), 0755) == 0);

    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_INIT_SCRIPT", restart_script);
    env.set("AIDEN_AGENT_RESTART_TEST_LOG", restart_log);
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"openai-whisper\",\"api_key\":\"sk-live\",\"model\":\"whisper-1\"},"
        "\"audio_values\":{\"socket\":\"/tmp/audio.sock\",\"sample_rate\":16000,\"channels\":1,\"bit_width\":16}}";
    REQUIRE(http_request(handle->port, "POST", "/api/config/test/stt/start", start_body).status == 200);
    REQUIRE(http_request(handle->port, "POST", "/api/system/env", "{\"system_env\":\"\"}").status == 200);

    std::this_thread::sleep_for(std::chrono::milliseconds(100));
    CHECK(::access(restart_log.c_str(), F_OK) != 0);

    REQUIRE(http_request(handle->port, "POST", "/api/config/test/stt/stop", "{}").status == 200);
    REQUIRE(wait_for_file_contains(restart_log, "restarted", 1000));
}

TEST_CASE("config_web: failed stt stop keeps the deferred agent restart pending") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string restart_script = tmp + "/restart-agent.sh";
    const std::string restart_log = tmp + "/restart.log";
    write_file(restart_script,
               "#!/bin/sh\n"
               "echo restarted >> \"$AIDEN_AGENT_RESTART_TEST_LOG\"\n");
    REQUIRE(::chmod(restart_script.c_str(), 0755) == 0);

    StubAgentHTTPServer agent_server(503);
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_INIT_SCRIPT", restart_script);
    env.set("AIDEN_AGENT_RESTART_TEST_LOG", restart_log);
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"openai-whisper\",\"api_key\":\"sk-live\",\"model\":\"whisper-1\"},"
        "\"audio_values\":{\"socket\":\"/tmp/audio.sock\",\"sample_rate\":16000,\"channels\":1,\"bit_width\":16}}";
    REQUIRE(http_request(handle->port, "POST", "/api/config/test/stt/start", start_body).status == 200);
    REQUIRE(http_request(handle->port, "POST", "/api/system/env", "{\"system_env\":\"\"}").status == 200);

    std::this_thread::sleep_for(std::chrono::milliseconds(100));
    REQUIRE(::access(restart_log.c_str(), F_OK) != 0);

    HttpResponse stop_response =
        http_request(handle->port, "POST", "/api/config/test/stt/stop", "{}");
    CHECK(stop_response.status == 503);

    // A failed stop means the live recording is still active. A later config
    // change must therefore remain deferred rather than restarting the agent.
    REQUIRE(http_request(handle->port, "POST", "/api/system/env",
                         "{\"system_env\":\"HTTP_PROXY=http://proxy.example:8080\\n\"}").status == 200);
    CHECK_FALSE(wait_for_file_contains(restart_log, "restarted", 1000));
}

TEST_CASE("config_web: stt live test leaves provider resolution to the running agent") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json",
               "{\"stt_providers\":{\"env-whisper\":{\"type\":\"openai-whisper\","
               "\"has_api_key\":true,\"model\":\"whisper-live-1\","
               "\"base_url\":\"https://stt.example.test/v1\"}},"
               "\"stt\":{\"provider\":\"env-whisper\",\"language\":\"en\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_STT_LIVE_TEST_API_KEY", "stale-process-env-key");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[stt_providers.env-whisper]\n"
               "type = \"openai-whisper\"\n"
               "api_key = \"$AIDEN_STT_LIVE_TEST_API_KEY\"\n"
               "model = \"whisper-live-1\"\n"
               "base_url = \"https://stt.example.test/v1\"\n");

    const std::string system_env_body =
        "{\"system_env\":\"AIDEN_STT_LIVE_TEST_API_KEY=stt-live-system-env-secret\\n\"}";
    REQUIRE(http_request(handle->port, "POST", "/api/system/env", system_env_body).status == 200);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"env-whisper\",\"language\":\"zh\"},"
        "\"audio_values\":{\"socket\":\"/tmp/live-audio.sock\",\"sample_rate\":16000,"
        "\"channels\":1,\"bit_width\":16,\"playback_backend\":\"audio_service\"}}";
    HttpResponse start_resp =
        http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);
    REQUIRE(start_resp.status == 200);

    const std::vector<CapturedHTTPRequest> requests = agent_server.requests();
    REQUIRE(requests.size() == 1);
    REQUIRE(requests[0].path == "/api/config-test/stt/start");

    cJSON* forwarded = cJSON_Parse(requests[0].body.c_str());
    REQUIRE(forwarded != nullptr);
    cJSON* stt_values = cJSON_GetObjectItem(forwarded, "stt_values");
    REQUIRE(stt_values != nullptr);
    CHECK(required_json_string(stt_values, "provider") == "env-whisper");
    CHECK(required_json_string(stt_values, "language") == "zh");
    CHECK(cJSON_GetObjectItem(stt_values, "api_key") == nullptr);
    CHECK(cJSON_GetObjectItem(stt_values, "model") == nullptr);
    CHECK(cJSON_GetObjectItem(stt_values, "base_url") == nullptr);

    cJSON* audio_values = cJSON_GetObjectItem(forwarded, "audio_values");
    REQUIRE(audio_values != nullptr);
    CHECK(required_json_string(audio_values, "socket") == "/tmp/live-audio.sock");
    CHECK(required_json_int(audio_values, "sample_rate") == 16000);
    CHECK(requests[0].body.find("stt-live-system-env-secret") == std::string::npos);
    CHECK(requests[0].body.find("stale-process-env-key") == std::string::npos);
    cJSON_Delete(forwarded);
}

TEST_CASE("config_web: stt live test rejects malformed provider fields before flattening") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json",
               "{\"stt_providers\":{\"env-whisper\":{\"type\":\"openai-whisper\","
               "\"api_key\":\"stored-key\",\"model\":\"whisper-1\"}},"
               "\"stt\":{\"provider\":\"env-whisper\",\"language\":\"en\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"env-whisper\",\"api_key\":false},"
        "\"audio_values\":{\"socket\":\"/tmp/live-audio.sock\",\"sample_rate\":16000,"
        "\"channels\":1,\"bit_width\":16,\"playback_backend\":\"audio_service\"}}";
    HttpResponse start_resp =
        http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);

    CHECK(start_resp.status == 400);
    CHECK(start_resp.body.find("stt_values.api_key") != std::string::npos);
    CHECK(agent_server.requests().empty());
}

TEST_CASE("config_web: stt live test does not load provider config in config web") {
    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_BIN", "/nonexistent/aiden-agent");
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"env-whisper\",\"language\":\"en\"},"
        "\"audio_values\":{\"socket\":\"/tmp/live-audio.sock\",\"sample_rate\":16000,"
        "\"channels\":1,\"bit_width\":16,\"playback_backend\":\"audio_service\"}}";
    HttpResponse start_resp =
        http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);

    CHECK(start_resp.status == 200);
    const std::vector<CapturedHTTPRequest> requests = agent_server.requests();
    REQUIRE(requests.size() == 1);
    CHECK(requests[0].path == "/api/config-test/stt/start");
    CHECK(requests[0].body.find("\"provider\":\"env-whisper\"") != std::string::npos);
}

TEST_CASE("config_web: stt live test keeps bare providers independent of config loading") {
    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    env.set("AIDEN_AGENT_BIN", "/nonexistent/aiden-agent");
    auto handle = start_server(env);

    const std::string start_body =
        "{\"stt_values\":{\"provider\":\"openai-whisper\",\"api_key\":\"sk-live\","
        "\"model\":\"whisper-1\",\"base_url\":\"https://stt.example.test/v1\","
        "\"language\":\"en\"},"
        "\"audio_values\":{\"socket\":\"/tmp/live-audio.sock\",\"sample_rate\":16000,"
        "\"channels\":1,\"bit_width\":16,\"playback_backend\":\"audio_service\"}}";
    HttpResponse start_resp =
        http_request(handle->port, "POST", "/api/config/test/stt/start", start_body);

    CHECK(start_resp.status == 200);
    const std::vector<CapturedHTTPRequest> requests = agent_server.requests();
    REQUIRE(requests.size() == 1);
    CHECK(requests[0].body.find("\"provider\":\"openai-whisper\"") != std::string::npos);
    CHECK(requests[0].body.find("\"api_key\":\"sk-live\"") != std::string::npos);
}

TEST_CASE("config_web: GET /api/storage/status parses the agent state mirror") {
    StubEnv env;
    env.storage_state =
        "SD_PRESENT=1\n"
        "SD_MOUNTED=1\n"
        "SD_DEVICE=/dev/mmcblk2p1\n"
        "SD_MOUNTPOINT=/mnt/sdcard\n"
        "EFFECTIVE_MODE=2\n"
        "SD_TOTAL_BYTES=31914983424\n"
        "SD_FREE_BYTES=31834701824\n"
        "REASON=\n"
        "FORMAT_STATUS=idle\n"
        "FORMAT_FS=\n"
        "FORMAT_ERROR=\n"
        "MIGRATE_STATUS=running\n"
        "MIGRATE_DETAIL=\n"
        "MIGRATE_ERROR=\n"
        "MIGRATE_MOVED_FILES=12\n"
        "MIGRATE_MOVED_BYTES=3145728\n";
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/storage/status");
    REQUIRE(resp.status == 200);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    CHECK((cJSON_GetObjectItem(parsed, "available")->type & 0xff) == cJSON_True);
    CHECK((cJSON_GetObjectItem(parsed, "sd_present")->type & 0xff) == cJSON_True);
    CHECK((cJSON_GetObjectItem(parsed, "sd_mounted")->type & 0xff) == cJSON_True);
    CHECK(required_json_string(parsed, "mount_point") == "/mnt/sdcard");
    CHECK(cJSON_GetObjectItem(parsed, "effective_mode")->valueint == 2);
    CHECK(cJSON_GetObjectItem(parsed, "total_bytes")->valuedouble == doctest::Approx(31914983424.0));
    cJSON* job = cJSON_GetObjectItem(parsed, "format_job");
    REQUIRE(job != nullptr);
    CHECK(required_json_string(job, "status") == "idle");
    cJSON* migration = cJSON_GetObjectItem(parsed, "migration");
    REQUIRE(migration != nullptr);
    CHECK(required_json_string(migration, "status") == "running");
    CHECK(cJSON_GetObjectItem(migration, "moved_files")->valueint == 12);
    CHECK(cJSON_GetObjectItem(migration, "moved_bytes")->valuedouble == doctest::Approx(3145728.0));
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/storage/status reports unavailable without a state file") {
    StubEnv env;  // no storage_state -> --storage-state points at a missing file
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/storage/status");
    REQUIRE(resp.status == 200);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    CHECK((cJSON_GetObjectItem(parsed, "available")->type & 0xff) == cJSON_False);
    CHECK((cJSON_GetObjectItem(parsed, "sd_present")->type & 0xff) == cJSON_False);
    CHECK(cJSON_GetObjectItem(parsed, "effective_mode")->valueint == 1);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: POST /api/storage/eject proxies to the agent") {
    StubAgentHTTPServer agent_server;
    StubEnv env;
    env.set("AIDEN_AGENT_HTTP_BASE_URL", "http://127.0.0.1:" + std::to_string(agent_server.port()));
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "POST", "/api/storage/eject", "{}");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("\"effective_mode\":1") != std::string::npos);

    auto requests = agent_server.requests();
    REQUIRE(requests.size() == 1);
    CHECK(requests[0].method == "POST");
    CHECK(requests[0].path == "/api/storage/eject");
}

TEST_CASE("config_web: GET /api/config accepts optional field-level omissions from resolved config") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string partial_config = remove_nested_key(
        resolved_config_json("duckduckgo", false),
        "hid",
        "android_keyboard_device"
    );
    write_file(tmp + "/config.json", partial_config);
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
    CHECK(config_error == nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* model = cJSON_GetObjectItem(config, "model");
    REQUIRE(model != nullptr);
    cJSON* model_name = cJSON_GetObjectItem(model, "model");
    REQUIRE(model_name != nullptr);
    cJSON* hid = cJSON_GetObjectItem(config, "hid");
    REQUIRE(hid != nullptr);
    cJSON* android_keyboard_device = cJSON_GetObjectItem(hid, "android_keyboard_device");
    REQUIRE(android_keyboard_device != nullptr);
    REQUIRE(android_keyboard_device->valuestring != nullptr);
    CHECK(std::string(android_keyboard_device->valuestring) == "/dev/hidg2");
    REQUIRE(model_name->valuestring != nullptr);
    CHECK(std::string(model_name->valuestring) == "bytedance-seed/seed-2.0-lite");
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config rejects missing required resolved config fields") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string partial_config = remove_nested_key(
        resolved_config_json("duckduckgo", false),
        "model",
        "provider"
    );
    write_file(tmp + "/config.json", partial_config);
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
    REQUIRE(config_error != nullptr);
    REQUIRE(config_error->valuestring != nullptr);
    CHECK(std::string(config_error->valuestring).find("model.provider") != std::string::npos);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config rejects missing paid search key presence sentinel") {
    const char* providers[] = {"brave", "brave-free", "tavily", NULL};
    for (int i = 0; providers[i]; ++i) {
        auto tmp = make_temp_dir();
        auto cleanup = std::unique_ptr<void, void(*)(void*)>(
            const_cast<char*>(tmp.c_str()),
            [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
        );
        const std::string partial_config = remove_nested_key(
            resolved_config_json(providers[i], true),
            "search",
            "has_api_key"
        );
        write_file(tmp + "/config.json", partial_config);
        StubEnv env;
        env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
        auto handle = start_server(env);
        HttpResponse resp = http_request(handle->port, "GET", "/api/config");
        CHECK(resp.status == 200);

        cJSON* parsed = cJSON_Parse(resp.body.c_str());
        REQUIRE(parsed != nullptr);
        cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
        REQUIRE(config_error != nullptr);
        REQUIRE(config_error->valuestring != nullptr);
        CHECK(std::string(config_error->valuestring).find("search.has_api_key") != std::string::npos);
        cJSON_Delete(parsed);
    }
}

TEST_CASE("config_web: GET /api/config accepts section-level omissions from resolved config") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    std::string partial_config = resolved_config_json("duckduckgo", false);
    partial_config = remove_top_level_key(partial_config, "audio_archive");
    write_file(tmp + "/config.json", partial_config);
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
    CHECK(config_error == nullptr);
    cJSON* config = cJSON_GetObjectItem(parsed, "config");
    REQUIRE(config != nullptr);
    cJSON* search = cJSON_GetObjectItem(config, "search");
    REQUIRE(search != nullptr);
    cJSON* provider = cJSON_GetObjectItem(search, "provider");
    REQUIRE(provider != nullptr);
    REQUIRE(provider->valuestring != nullptr);
    CHECK(std::string(provider->valuestring) == "duckduckgo");
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config rejects non-object resolved config sections") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string invalid_config = replace_top_level_key_with_array(
        resolved_config_json("duckduckgo", false),
        "model"
    );
    write_file(tmp + "/config.json", invalid_config);
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);
    CHECK(resp.body.find("model: expected object") != std::string::npos);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
    REQUIRE(config_error != nullptr);
    REQUIRE(config_error->valuestring != nullptr);
    CHECK(std::string(config_error->valuestring).find("model: expected object") != std::string::npos);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config/meta returns 503 when stub returns invalid JSON") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/meta.txt", "this is not json\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", tmp + "/meta.txt");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(resp.status == 503);
    CHECK(resp.body.find("unexpected response") != std::string::npos);
}

TEST_CASE("config_web: GET /api/config/meta returns 503 when stub exits non-zero") {
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_EXIT", "1");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(resp.status == 503);
}

TEST_CASE("config_web: GET /api/config/meta supports quoted agent binary paths") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string agent_bin = tmp + "/agent stub's cli";
    REQUIRE(::symlink(AIDEN_AGENT_STUB_BIN, agent_bin.c_str()) == 0);

    StubEnv env;
    env.set("AIDEN_AGENT_BIN", agent_bin);
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(resp.status == 200);
}

TEST_CASE("config_web: POST /api/config derives field types from config metadata") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/meta.json",
               "{\"sections\":["
               "{\"name\":\"model\",\"fields\":["
               "{\"key\":\"provider\",\"widget\":\"select\"},"
               "{\"key\":\"model\",\"widget\":\"text\"},"
               "{\"key\":\"max_response_tokens\",\"widget\":\"boolean\"}]}"
               "]}\n");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", tmp + "/meta.json");
    auto handle = start_server(env);
    HttpResponse meta = http_request(handle->port, "GET", "/api/config/meta");
    REQUIRE(meta.status == 200);

    // Successful metadata is cached and shared by GET and save validation.
    // Replacing the backing file must not create a second schema source.
    write_file(tmp + "/meta.json", "not json anymore\n");
    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\","
        "\"max_response_tokens\":true}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
}

TEST_CASE("config_web: POST /api/config maps metadata widgets to JSON types") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/meta.json",
               "{\"sections\":["
               "{\"name\":\"storage\",\"fields\":["
               "{\"key\":\"monitor_enabled\",\"widget\":\"boolean\"},"
               "{\"key\":\"min_card_free_mb\",\"widget\":\"number\"}]},"
               "{\"name\":\"telemetry\",\"fields\":["
               "{\"key\":\"tags\",\"widget\":\"list\"}]},"
               "{\"name\":\"tts\",\"fields\":["
               "{\"key\":\"speed\",\"widget\":\"select\","
               "\"range\":{\"min\":0.5,\"max\":2,\"step\":0.1}}]},"
               "{\"name\":\"tts_providers\",\"fields\":["
               "{\"key\":\"type\",\"widget\":\"select\"},"
               "{\"key\":\"api_key\",\"widget\":\"text\"}]},"
               "{\"name\":\"agent\",\"fields\":["
               "{\"key\":\"custom_instruction\",\"widget\":\"textarea\"}]}"
               "]}\n");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", tmp + "/meta.json");
    auto handle = start_server(env);
    struct InvalidCase {
        const char* config;
        const char* path;
        const char* expected_type;
    };
    const InvalidCase cases[] = {
        {"{\"storage\":{\"monitor_enabled\":\"yes\"}}", "storage.monitor_enabled", "bool"},
        {"{\"storage\":{\"min_card_free_mb\":\"64\"}}", "storage.min_card_free_mb", "number"},
        {"{\"telemetry\":{\"tags\":\"alpha\"}}", "telemetry.tags", "array"},
        {"{\"tts\":{\"speed\":\"fast\"}}", "tts.speed", "number"},
        {"{\"tts_providers\":{\"work\":{\"type\":7}}}", "tts_providers.work.type", "string"},
        {"{\"tts_providers\":{\"work\":{\"api_key\":7}}}", "tts_providers.work.api_key", "string"},
        {"{\"tts\":{\"api_key\":7}}", "tts.api_key", "string"},
        {"{\"agent\":{\"custom_instruction\":7}}", "agent.custom_instruction", "string"},
    };

    for (const InvalidCase& test_case : cases) {
        const std::string body = std::string("{\"config\":") + test_case.config +
                                 ",\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        CHECK_MESSAGE(resp.status == 400, test_case.path << ": status=" << resp.status);
        CHECK_MESSAGE(resp.body.find(test_case.path) != std::string::npos,
                      test_case.path << ": body=" << resp.body);
        CHECK_MESSAGE(resp.body.find(std::string("expected ") + test_case.expected_type) != std::string::npos,
                      test_case.path << ": body=" << resp.body);
    }
}

TEST_CASE("config_web: POST /api/config keeps compatibility field type guards") {
    StubEnv env;
    auto handle = start_server(env);
    struct InvalidCase {
        const char* config;
        const char* path;
        const char* expected_type;
    };
    const InvalidCase cases[] = {
        {"{\"device\":{\"backend\":7}}", "device.backend", "string"},
        {"{\"search\":{\"has_api_key\":\"yes\"}}", "search.has_api_key", "bool"},
        {"{\"termination_policy\":{\"enabled\":\"yes\"}}", "termination_policy.enabled", "bool"},
        {"{\"live_activity\":{\"enabled\":\"yes\"}}", "live_activity.enabled", "bool"},
    };

    for (const InvalidCase& test_case : cases) {
        const std::string body = std::string("{\"config\":") + test_case.config +
                                 ",\"apply_wifi\":false}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
        CHECK_MESSAGE(resp.status == 400, test_case.path << ": status=" << resp.status);
        CHECK_MESSAGE(resp.body.find(test_case.path) != std::string::npos,
                      test_case.path << ": body=" << resp.body);
        CHECK_MESSAGE(resp.body.find(std::string("expected ") + test_case.expected_type) != std::string::npos,
                      test_case.path << ": body=" << resp.body);
    }
}

TEST_CASE("config_web: POST /api/config returns 503 for unusable config metadata") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/meta.json",
               "{\"sections\":[{\"name\":\"agent\",\"fields\":["
               "{\"key\":\"input_mode\",\"widget\":\"unknown\"}]}]}\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_META_FILE", tmp + "/meta.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(
        handle->port, "POST", "/api/config",
        "{\"config\":{\"agent\":{\"input_mode\":\"text\"}},\"apply_wifi\":false}");
    CHECK(resp.status == 503);
    CHECK(resp.body.find("unsupported widget") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config returns 200 when stub config-check approves") {
    StubEnv env;  // defaults: check returns valid:true
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
}

TEST_CASE("config_web: POST /api/config rejects non-object termination_policy") {
    StubEnv env;
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"termination_policy\":123,\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("termination_policy: expected object") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects non-object device section") {
    StubEnv env;
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":123,"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("device: expected object") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config legacy wifi fields update saved networks") {
    StubEnv env;  // defaults: check returns valid:true
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/wifi.conf",
        "ctrl_interface=/var/run/wpa_supplicant\n"
        "update_config=1\n"
        "country=CN\n"
        "\n"
        "network={\n"
        "    ssid=787878\n"
        "    psk=\"xxx-password\"\n"
        "    priority=1\n"
        "    scan_ssid=1\n"
        "}\n"
        "\n"
        "network={\n"
        "    ssid=797979\n"
        "    psk=\"yyy-password\"\n"
        "    priority=2\n"
        "    scan_ssid=1\n"
        "}\n");

    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},"
        "\"wifi\":{\"ssid\":\"zzz\",\"psk\":\"zzz-password\",\"country\":\"CN\"},"
        "\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/wifi.conf");
    CHECK(saved.find("ssid=787878") != std::string::npos);
    CHECK(saved.find("psk=\"xxx-password\"") != std::string::npos);
    CHECK(saved.find("ssid=797979") != std::string::npos);
    CHECK(saved.find("psk=\"yyy-password\"") != std::string::npos);
    CHECK(saved.find("ssid=7a7a7a") != std::string::npos);
    CHECK(saved.find("psk=\"zzz-password\"") != std::string::npos);
    CHECK(saved.find("ssid=7a7a7a\n    psk=\"zzz-password\"\n    scan_ssid=1\n    priority=3") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config returns 400 when stub config-check rejects with reasons") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/check.json",
               "{\"valid\":false,\"errors\":[{\"field\":\"x\","
               "\"message\":\"stub \\\"rejected\\\" at C:\\\\config\\nline\"}]}\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CHECK_FILE", tmp + "/check.json");
    env.set("AIDEN_AGENT_STUB_CHECK_EXIT", "1");
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* error = cJSON_GetObjectItem(parsed, "error");
    REQUIRE(error != nullptr);
    REQUIRE(error->valuestring != nullptr);
    CHECK(std::string(error->valuestring) == "stub \"rejected\" at C:\\config\nline");
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: POST /api/config returns 503 when stub config-check returns invalid JSON") {
    // Regression guard for PR #165: a broken validator must surface as 503,
    // matching the GET /api/config/meta contract. Previously every save-time
    // failure (missing binary, garbled output, timeout) collapsed to 400,
    // mislabelling a server outage as a user error.
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/check.txt", "not json at all\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_UPDATE_FILE", tmp + "/check.txt");
    env.set("AIDEN_AGENT_STUB_UPDATE_EXIT", "1");
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 503);
    CHECK(resp.body.find("invalid JSON") != std::string::npos);
}

TEST_CASE("config_web: both endpoints fail closed with 503 when AIDEN_AGENT_BIN points nowhere") {
    StubEnv env;
    // Override the stub injection: start_server will skip its default when
    // the scenario already supplies AIDEN_AGENT_BIN, so this path wins
    // unambiguously.
    env.set("AIDEN_AGENT_BIN", "/nonexistent/aiden-fake-agent");
    auto handle = start_server(env);

    HttpResponse meta = http_request(handle->port, "GET", "/api/config/meta");
    CHECK(meta.status == 503);
    CHECK(meta.body.find("agent binary not found") != std::string::npos);

    const std::string body =
        "{\"config\":{\"device\":{\"device_type\":\"iOS\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse save = http_request(handle->port, "POST", "/api/config", body);
    CHECK(save.status == 503);
    CHECK(save.body.find("agent binary not found") != std::string::npos);
}

TEST_CASE("config_web: failed manual OTA update releases launch lock even if a child keeps running") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );

    const std::string env_run_path = tmp + "/aiden-env-run";
    const std::string ota_path = tmp + "/ota";
    const std::string lock_path = tmp + "/config_web_ota_update.lock";
    const std::string log_path = tmp + "/config_web_ota_update.log";

    write_file(env_run_path, "#!/bin/sh\nexec \"$@\"\n");
    REQUIRE(::chmod(env_run_path.c_str(), 0755) == 0);

    write_file(ota_path,
               "#!/bin/sh\n"
               "if [ \"${1:-}\" = \"update\" ]; then\n"
               "  (sleep 3) &\n"
               "  echo 'stub update failed' >&2\n"
               "  exit 1\n"
               "fi\n"
               "exit 0\n");
    REQUIRE(::chmod(ota_path.c_str(), 0755) == 0);

    StubEnv env;
    env.set("AIDEN_OTA_BIN", ota_path);
    env.set("AIDEN_ENV_RUN_BIN", env_run_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOCK", lock_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", log_path);
    auto handle = start_server(env);

    HttpResponse first = http_request(handle->port, "POST", "/api/ota/update");
    REQUIRE(first.status == 200);
    REQUIRE(wait_for_file_contains(log_path,
                                   "[config_web] [ota] update_exited exit_code=1", 2000));

    HttpResponse retry;
    bool accepted = false;
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(800);
    while (std::chrono::steady_clock::now() < deadline) {
        retry = http_request(handle->port, "POST", "/api/ota/update");
        if (retry.status == 200) {
            accepted = true;
            break;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(50));
    }
    CHECK(accepted);
    CHECK(retry.body.find("ota update already running") == std::string::npos);
}

TEST_CASE("config_web: manual OTA update appends update log and reports start size") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );

    const std::string env_run_path = tmp + "/aiden-env-run";
    const std::string ota_path = tmp + "/ota";
    const std::string lock_path = tmp + "/config_web_ota_update.lock";
    const std::string log_path = tmp + "/config_web_ota_update.log";
    const std::string previous_log = "previous web ota update\n";
    write_file(log_path, previous_log);

    write_file(env_run_path, "#!/bin/sh\nexec \"$@\"\n");
    REQUIRE(::chmod(env_run_path.c_str(), 0755) == 0);

    write_file(ota_path,
               "#!/bin/sh\n"
               "if [ \"${1:-}\" = \"update\" ]; then\n"
               "  echo 'new ota update output'\n"
               "  exit 0\n"
               "fi\n"
               "exit 0\n");
    REQUIRE(::chmod(ota_path.c_str(), 0755) == 0);

    StubEnv env;
    env.set("AIDEN_OTA_BIN", ota_path);
    env.set("AIDEN_ENV_RUN_BIN", env_run_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOCK", lock_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", log_path);
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "POST", "/api/ota/update");
    REQUIRE(resp.status == 200);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    CHECK(required_json_int(parsed, "ota_log_start_size_bytes") == static_cast<int>(previous_log.size()));
    cJSON_Delete(parsed);

    REQUIRE(wait_for_file_contains_any(log_path,
                                       {
                                           "[config_web] ota update exited rc=0",
                                           "[config_web] [ota] update_exited exit_code=0",
                                       },
                                       2000));
    const std::string log = read_file(log_path);
    CHECK(log.find(previous_log) == 0);
    CHECK(log.find("new ota update output") != std::string::npos);
}

TEST_CASE("config_web: manual OTA update truncates oversized update log before appending") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );

    const std::string env_run_path = tmp + "/aiden-env-run";
    const std::string ota_path = tmp + "/ota";
    const std::string lock_path = tmp + "/config_web_ota_update.lock";
    const std::string log_path = tmp + "/config_web_ota_update.log";
    write_file(log_path, std::string(1024 * 1024, 'x'));

    write_file(env_run_path, "#!/bin/sh\nexec \"$@\"\n");
    REQUIRE(::chmod(env_run_path.c_str(), 0755) == 0);

    write_file(ota_path,
               "#!/bin/sh\n"
               "if [ \"${1:-}\" = \"update\" ]; then\n"
               "  echo 'fresh ota update output'\n"
               "  exit 0\n"
               "fi\n"
               "exit 0\n");
    REQUIRE(::chmod(ota_path.c_str(), 0755) == 0);

    StubEnv env;
    env.set("AIDEN_OTA_BIN", ota_path);
    env.set("AIDEN_ENV_RUN_BIN", env_run_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOCK", lock_path);
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", log_path);
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "POST", "/api/ota/update");
    REQUIRE(resp.status == 200);
    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    CHECK(required_json_int(parsed, "ota_log_start_size_bytes") == 0);
    cJSON_Delete(parsed);

    REQUIRE(wait_for_file_contains_any(log_path,
                                       {
                                           "[config_web] ota update exited rc=0",
                                           "[config_web] [ota] update_exited exit_code=0",
                                       },
                                       2000));
    const std::string log = read_file(log_path);
    CHECK(log.size() < 4096);
    CHECK(log.find(std::string(64, 'x')) == std::string::npos);
    CHECK(log.find("fresh ota update output") != std::string::npos);
}

TEST_CASE("config_web: GET /api/ota/logs keeps update and health logs separate") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );

    const std::string update_log_path = tmp + "/config_web_ota_update.log";
    const std::string health_log_path = tmp + "/ota_health.log";
    const std::string update_log =
        "2026-08-05T06:22:02Z [INFO] [config_web] [ota] update_requested\n"
        "2026-08-05T06:22:03Z [ERROR] [config_web] [ota] update_exited exit_code=1\n";
    std::ostringstream health_log;
    for (int i = 0; i < 5000; ++i) {
        health_log << "ota health previous boot line " << i << "\n";
    }
    health_log << "health timeout waiting for /userdata/ota/health.ok\n";
    write_file(update_log_path, update_log);
    write_file(health_log_path, health_log.str());

    StubEnv env;
    env.set("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", update_log_path);
    env.set("AIDEN_CONFIG_WEB_OTA_HEALTH_LOG", health_log_path);
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/ota/logs");
    REQUIRE(resp.status == 200);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* update = cJSON_GetObjectItem(parsed, "ota_log");
    cJSON* health = cJSON_GetObjectItem(parsed, "ota_health_log");
    REQUIRE(update != nullptr);
    REQUIRE(health != nullptr);

    CHECK(required_json_string(update, "path") == update_log_path);
    CHECK(required_json_int(update, "size_bytes") == static_cast<int>(update_log.size()));
    CHECK(required_json_string(update, "log").find(
              "[config_web] [ota] update_exited exit_code=1") != std::string::npos);
    CHECK(required_json_string(update, "log").find("health timeout") == std::string::npos);

    CHECK(required_json_string(health, "path") == health_log_path);
    CHECK(required_json_string(health, "log").find("health timeout waiting for /userdata/ota/health.ok") != std::string::npos);
    CHECK(required_json_string(health, "log").find("[config_web] [ota]") == std::string::npos);
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: exports llm raw log files without JSON wrapping") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    const std::string name = "llm-http-20260624153000123.log";
    const std::string content =
        "{\"kind\":\"request\",\"ts\":\"2026-06-24T15:30:00Z\"}\n"
        "{\"kind\":\"response\",\"status\":200}\n";
    write_file(log_dir + "/" + name, content);

    HttpResponse resp = http_request(handle->port, "GET", "/api/llm-logs/export/" + name);
    CHECK(resp.status == 200);
    CHECK(resp.body == content);
}

TEST_CASE("config_web: exports support log archive with langfuse agent and http logs") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    write_file(log_dir + "/llm-http-20260701070000123.log", "old http log\n");
    write_file(log_dir + "/llm-http-20260701080000123.log", "new http log\n");

    const std::string memory_dir = handle->tmp_dir + "/memory";
    const std::string episodes_dir = memory_dir + "/episodes";
    const std::string year_dir = episodes_dir + "/2026";
    const std::string old_episode_dir = year_dir + "/ep_001_old";
    const std::string new_episode_dir = year_dir + "/ep_999_new";
    REQUIRE(::mkdir(memory_dir.c_str(), 0755) == 0);
    REQUIRE(::mkdir(episodes_dir.c_str(), 0755) == 0);
    REQUIRE(::mkdir(year_dir.c_str(), 0755) == 0);
    REQUIRE(::mkdir(old_episode_dir.c_str(), 0755) == 0);
    REQUIRE(::mkdir(new_episode_dir.c_str(), 0755) == 0);
    write_file(old_episode_dir + "/episode.yaml", "id: old\n");
    write_file(new_episode_dir + "/episode.yaml", "id: new\nkind: langfuse-source\n");

    HttpResponse resp = http_request(handle->port, "GET", "/api/logs/export");
    REQUIRE(resp.status == 200);
    REQUIRE(resp.body.size() > 2);
    CHECK(static_cast<unsigned char>(resp.body[0]) == 0x1f);
    CHECK(static_cast<unsigned char>(resp.body[1]) == 0x8b);

    const std::string archive_path = handle->tmp_dir + "/support-logs.tar.gz";
    write_binary_file(archive_path, resp.body);
    const std::string archive = shell_quote(archive_path);
    const std::string entries = run_command_capture("LC_ALL=C tar -tzf " + archive);
    CHECK(entries.find("langfuse.yaml\n") != std::string::npos);
    CHECK(entries.find("agent.log\n") != std::string::npos);
    // Expect the original LLM log filename, not the generic http.log
    CHECK(entries.find("llm-http-20260701080000123.log\n") != std::string::npos);

    const std::string http_log = run_command_capture("LC_ALL=C tar -xOzf " + archive + " llm-http-20260701080000123.log");
    CHECK(http_log.find("new http log") != std::string::npos);

    const std::string langfuse = run_command_capture("LC_ALL=C tar -xOzf " + archive + " langfuse.yaml");
    CHECK(langfuse.find("id: new") != std::string::npos);
    CHECK(langfuse.find("kind: langfuse-source") != std::string::npos);
}

TEST_CASE("config_web: support log archive caps staged http log tail") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    std::string content = "begin-marker\n";
    content.append(5 * 1024 * 1024, 'x');
    content += "\nend-marker\n";
    write_file(log_dir + "/llm-http-20260701090000123.log", content);

    HttpResponse resp = http_request(handle->port, "GET", "/api/logs/export");
    REQUIRE(resp.status == 200);

    const std::string archive_path = handle->tmp_dir + "/support-logs-large.tar.gz";
    write_binary_file(archive_path, resp.body);
    const std::string archive = shell_quote(archive_path);
    const std::string http_log = run_command_capture("LC_ALL=C tar -xOzf " + archive + " llm-http-20260701090000123.log");
    CHECK(http_log.find("# truncated: copied latest ") != std::string::npos);
    CHECK(http_log.find("begin-marker") == std::string::npos);
    CHECK(http_log.find("end-marker") != std::string::npos);
    CHECK(http_log.size() < content.size());
}

TEST_CASE("config_web: support log archive falls back to http.log when no LLM log exists") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    // Do not create any llm-http-*.log files

    const std::string memory_dir = handle->tmp_dir + "/memory";
    REQUIRE(::mkdir(memory_dir.c_str(), 0755) == 0);

    HttpResponse resp = http_request(handle->port, "GET", "/api/logs/export");
    REQUIRE(resp.status == 200);

    const std::string archive_path = handle->tmp_dir + "/support-logs-nohttp.tar.gz";
    write_binary_file(archive_path, resp.body);
    const std::string archive = shell_quote(archive_path);
    const std::string entries = run_command_capture("LC_ALL=C tar -tzf " + archive);
    // When no LLM log exists, should fall back to http.log
    CHECK(entries.find("http.log\n") != std::string::npos);

    const std::string http_log = run_command_capture("LC_ALL=C tar -xOzf " + archive + " http.log");
    // Should contain placeholder text
    CHECK(http_log.find("HTTP log unavailable") != std::string::npos);
}

TEST_CASE("config_web: legacy llm log JSON file endpoint is removed") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    const std::string name = "llm-http-20260624154500999.log";
    write_file(log_dir + "/" + name, "{\"kind\":\"request\"}\n");

    HttpResponse resp = http_request(handle->port, "GET", "/api/llm-logs/file/" + name);
    CHECK(resp.status == 404);
}

TEST_CASE("config_web: exports llm raw log files larger than viewer limit") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    const std::string name = "llm-http-20260624170000999.log";
    const std::string content(16 * 1024 * 1024 + 123, 'x');
    write_file(log_dir + "/" + name, content);

    HttpResponse resp = http_request(handle->port, "GET", "/api/llm-logs/export/" + name);
    CHECK(resp.status == 200);
    CHECK(resp.body.size() == content.size());
    CHECK(resp.body.compare(0, 128, content, 0, 128) == 0);
    CHECK(resp.body.compare(resp.body.size() - 128, 128, content, content.size() - 128, 128) == 0);
}

TEST_CASE("config_web: reports server errors for unreadable llm raw log files") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string log_dir = handle->tmp_dir + "/log";
    REQUIRE(::mkdir(log_dir.c_str(), 0755) == 0);
    const std::string name = "llm-http-20260624173000999.log";
    const std::string path = log_dir + "/" + name;
    write_file(path, "{\"kind\":\"request\"}\n");
    REQUIRE(::chmod(path.c_str(), 0000) == 0);

    HttpResponse resp = http_request(handle->port, "GET", "/api/llm-logs/export/" + name);
    CHECK(resp.status == 500);
    CHECK(resp.body.find("failed to open log file") != std::string::npos);

    REQUIRE(::chmod(path.c_str(), 0644) == 0);
}

TEST_CASE("config_web: imports llm raw log files into the viewer log directory") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string name = "llm-http-import-session.log";
    const std::string content =
        "{\"kind\":\"request\",\"ts\":\"2026-06-24T16:00:00Z\",\"body\":\"hello\"}\n"
        "{\"kind\":\"response\",\"status\":500,\"body\":\"upstream failed\"}\n";

    HttpResponse import_resp = http_request(handle->port, "POST", "/api/llm-logs/import/" + name, content);
    REQUIRE(import_resp.status == 200);

    const std::string imported_path = handle->tmp_dir + "/log/" + name;
    const std::string imported = read_file(imported_path);
    const size_t sample_size = std::min<size_t>(128, content.size());
    CHECK(imported.size() == content.size());
    CHECK(imported.compare(0, sample_size, content, 0, sample_size) == 0);
    CHECK(imported.compare(imported.size() - sample_size, sample_size, content, content.size() - sample_size, sample_size) == 0);

    HttpResponse list_resp = http_request(handle->port, "GET", "/api/llm-logs");
    REQUIRE(list_resp.status == 200);
    CHECK(list_resp.body.find(name) != std::string::npos);

    HttpResponse export_resp = http_request(handle->port, "GET", "/api/llm-logs/export/" + name);
    CHECK(export_resp.status == 200);
    CHECK(export_resp.body == content);
}

TEST_CASE("config_web: imports llm raw log files larger than viewer limit") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string name = "llm-http-import-large.log";
    const std::string content(16 * 1024 * 1024 + 321, 'y');

    HttpResponse import_resp = http_request(handle->port, "POST", "/api/llm-logs/import/" + name, content);
    REQUIRE(import_resp.status == 200);

    const std::string imported_path = handle->tmp_dir + "/log/" + name;
    CHECK(read_file(imported_path) == content);

    HttpResponse export_resp = http_request(handle->port, "GET", "/api/llm-logs/export/" + name);
    CHECK(export_resp.status == 200);
    CHECK(export_resp.body.size() == content.size());
    CHECK(export_resp.body.compare(0, 128, content, 0, 128) == 0);
    CHECK(export_resp.body.compare(export_resp.body.size() - 128, 128, content, content.size() - 128, 128) == 0);
}

TEST_CASE("config_web: POST /api/config writes named voice providers") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{"
        "\"tts_providers\":{"
        "\"minimax-main\":{\"provider\":\"minimax\",\"api_key\":\"sk-mm-1234\","
        "\"voice_id\":\"male-qn-qingse\",\"emotion\":\"happy\"},"
        "\"fish\":{\"provider\":\"fish-audio\",\"api_key\":\"$FISH_KEY\","
        "\"reference_id\":\"ref-abc\",\"model\":\"s2-pro\"}},"
        "\"stt_providers\":{"
        "\"tencent\":{\"provider\":\"tencent-asr\",\"app_id\":\"1234\","
        "\"secret_id\":\"AKID-xxx\",\"secret_key\":\"secret-yyy\",\"region\":\"ap-shanghai\"}},"
        "\"tts\":{\"provider\":\"minimax-main\",\"speed\":1.2},"
        "\"stt\":{\"provider\":\"tencent\",\"language\":\"zh\"},"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[tts_providers.minimax-main]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-mm-1234\"") != std::string::npos);
    CHECK(saved.find("voice_id = \"male-qn-qingse\"") != std::string::npos);
    CHECK(saved.find("[tts_providers.fish]") != std::string::npos);
    CHECK(saved.find("api_key = \"$FISH_KEY\"") != std::string::npos);
    CHECK(saved.find("reference_id = \"ref-abc\"") != std::string::npos);
    CHECK(saved.find("[stt_providers.tencent]") != std::string::npos);
    CHECK(saved.find("app_id = \"1234\"") != std::string::npos);
    CHECK(saved.find("secret_key = \"secret-yyy\"") != std::string::npos);
    // The reference is stored unresolved: the Go runtime resolves it.
    CHECK(saved.find("provider = \"minimax-main\"") != std::string::npos);
    CHECK(saved.find("provider = \"tencent\"\n") != std::string::npos);
}

// This is the exact bug class that broke named provider records: a save of an unrelated
// section carried no records key, the read path started from an empty map, and
// every record was erased from agent.toml.
TEST_CASE("config_web: POST /api/config keeps voice records when the payload omits them") {
    // The stored config comes from the agent CLI's resolved output, so records
    // are seeded through the stub's config file rather than by a prior POST --
    // a POST writes agent.toml, which the stub does not read back.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"fish\":{\"provider\":\"fish-audio\",\"api_key\":\"sk-fish-1234\"}},"
               "\"stt_providers\":{\"whisper\":{\"provider\":\"openai-whisper\",\"api_key\":\"sk-w-1234\"}},"
               "\"tts\":{\"provider\":\"fish\",\"speed\":1},"
               "\"stt\":{\"provider\":\"whisper\",\"language\":\"zh\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    // A save that touches only [agent], with no voice keys at all.
    const std::string unrelated =
        "{\"config\":{"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},"
        "\"agent\":{\"locale\":\"en-US\"}},\"apply_wifi\":false}";
    CHECK(http_request(handle->port, "POST", "/api/config", unrelated).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("[tts_providers.fish]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-fish-1234\"") != std::string::npos);
    CHECK(saved.find("[stt_providers.whisper]") != std::string::npos);
    CHECK(saved.find("api_key = \"sk-w-1234\"") != std::string::npos);
}

// Voice-provider credentials are write-only. An empty edit omits the key, and
// the backend must preserve the stored value.
TEST_CASE("config_web: POST /api/config preserves an omitted voice provider api_key") {
    // Seeded through the stub config file: the stored config is the agent CLI's
    // resolved output, so a prior POST would not be visible to this GET.
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"fish\":{\"provider\":\"fish-audio\","
               "\"api_key\":\"sk-fish-secret-1234\"}},"
               "\"tts\":{\"provider\":\"fish\",\"speed\":1},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse get_resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(get_resp.status == 200);
    // The key must not come back in the clear.
    CHECK(get_resp.body.find("sk-fish-secret-1234") == std::string::npos);

    cJSON* root = cJSON_Parse(get_resp.body.c_str());
    REQUIRE(root != nullptr);
    cJSON* cfg_obj = cJSON_GetObjectItem(root, "config");
    REQUIRE(cfg_obj != nullptr);
    cJSON* records = cJSON_GetObjectItem(cfg_obj, "tts_providers");
    REQUIRE(records != nullptr);
    cJSON* fish = cJSON_GetObjectItem(records, "fish");
    REQUIRE(fish != nullptr);
    CHECK(cJSON_GetObjectItem(fish, "api_key") == nullptr);
    cJSON* has_api_key = cJSON_GetObjectItem(fish, "has_api_key");
    REQUIRE(has_api_key != nullptr);
    CHECK((has_api_key->type & 0xff) == cJSON_True);
    cJSON_Delete(root);

    // Post the record without a credential, as the empty edit field does.
    const std::string repost =
        "{\"config\":{"
        "\"tts_providers\":{\"fish\":{\"provider\":\"fish-audio\"}},"
        "\"tts\":{\"provider\":\"fish\",\"speed\":1.5},"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    CHECK(http_request(handle->port, "POST", "/api/config", repost).status == 200);

    const std::string saved = read_file(handle->tmp_dir + "/agent.toml");
    CHECK(saved.find("api_key = \"sk-fish-secret-1234\"") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects an invalid voice provider name") {
    StubEnv env;
    auto handle = start_server(env);

    // A name save_agent_toml could not write back must fail the schema check
    // rather than producing a config that loads but never saves again.
    const std::string body =
        "{\"config\":{"
        "\"tts_providers\":{\"bad name\":{\"provider\":\"minimax\"}},"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("tts_providers") != std::string::npos);

    const std::string stt_body =
        "{\"config\":{"
        "\"stt_providers\":{\"bad.name\":{\"provider\":\"tencent-asr\"}},"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse stt_resp = http_request(handle->port, "POST", "/api/config", stt_body);
    CHECK(stt_resp.status == 400);
    CHECK(stt_resp.body.find("stt_providers") != std::string::npos);
}

TEST_CASE("config_web: POST /api/config rejects a non-object voice provider entry") {
    StubEnv env;
    auto handle = start_server(env);

    const std::string body =
        "{\"config\":{"
        "\"tts_providers\":{\"fish\":\"not-an-object\"},"
        "\"model\":{\"provider\":\"openrouter\",\"model\":\"x\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("tts_providers.fish") != std::string::npos);
}

TEST_CASE("config_web: provider config tests are delegated unchanged to agent") {
    struct Case {
        const char* section;
        const char* values;
    };
    const Case cases[] = {
        {"model", "{\"provider\":\"kimi\",\"model\":\"kimi-k3\"}"},
        {"tts", "{\"provider\":\"fish-audio\",\"api_key\":\"test-key\",\"reference_id\":\"ref\",\"speed\":1}"},
        {"stt", "{\"provider\":\"qwen-asr\",\"api_key\":\"test-key\",\"language\":\"zh\"}"},
    };

    for (const Case& c : cases) {
        const std::string tmp = make_temp_dir();
        auto cleanup = std::unique_ptr<void, void(*)(void*)>(
            const_cast<char*>(tmp.c_str()),
            [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
        );
        const std::string log_path = tmp + "/config-test.log";
        const std::string result_path = tmp + "/config-test-result.json";
        write_file(result_path,
                   "{\"ok\":true,\"results\":[{\"check\":\"provider_runtime\","
                   "\"passed\":true,\"detail\":\"tested by agent\"}]}\n");

        StubEnv env;
        env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
        env.set("AIDEN_AGENT_STUB_CONFIG_TEST_FILE", result_path);
        auto handle = start_server(env);
        write_file(handle->tmp_dir + "/system_env", "HTTPS_PROXY=http://127.0.0.1:9\n");

        const std::string body =
            std::string("{\"section\":\"") + c.section + "\",\"values\":" + c.values + "}";
        HttpResponse resp = http_request(handle->port, "POST", "/api/config/test", body);
        REQUIRE_MESSAGE(resp.status == 200, std::string(c.section));

        cJSON* parsed = cJSON_Parse(resp.body.c_str());
        REQUIRE(parsed != nullptr);
        cJSON* results = cJSON_GetObjectItem(parsed, "results");
        REQUIRE(results != nullptr);
        CHECK_MESSAGE(cJSON_GetArraySize(results) == 1, std::string(c.section));
        CHECK_MESSAGE(required_test_result(parsed, "provider_runtime") != nullptr, std::string(c.section));
        cJSON_Delete(parsed);

        REQUIRE_MESSAGE(wait_for_file_contains(log_path, "stdin:", 1000), std::string(c.section));
        const std::string log = read_file(log_path);
        CHECK_MESSAGE(log.find(std::string("--section=") + c.section) != std::string::npos,
                      std::string(c.section));
        CHECK_MESSAGE(log.find("stdin:\n" + body) != std::string::npos, std::string(c.section));
    }
}

TEST_CASE("config_web: model config test leaves provider references for agent") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"work-openai\":{\"type\":\"openai\","
               "\"api_key\":\"sk-model-secret-1234\",\"base_url\":\"http://127.0.0.1:9\"}},"
               "\"model\":{\"provider\":\"work-openai\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", tmp + "/config-test.log");
    auto handle = start_server(env);

    const std::string test_body =
        "{\"section\":\"model\",\"values\":{\"provider\":\"work-openai\",\"model\":\"gpt-4o\"}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(tmp + "/config-test.log", "stdin:", 1000));
    const std::string log = read_file(tmp + "/config-test.log");
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
    CHECK(log.find("sk-model-secret-1234") == std::string::npos);
    CHECK(log.find("http://127.0.0.1:9") == std::string::npos);
}

TEST_CASE("config_web: model config test resolves provider api_key from system env") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string log_path = tmp + "/config-test.log";
    const std::string base_url = "https://model.example.com/v1";
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"env-openai\":{\"type\":\"openai\","
               "\"has_api_key\":true,\"base_url\":\"" + base_url + "\"}},"
               "\"model\":{\"provider\":\"env-openai\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_ENV_KEY", "AIDEN_CONFIG_TEST_MODEL_API_KEY");
    env.set("AIDEN_CONFIG_TEST_MODEL_API_KEY", "process-env-key-must-not-win");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[model_providers.env-openai]\n"
               "type = \"openai\"\n"
               "api_key = \"$AIDEN_CONFIG_TEST_MODEL_API_KEY\"\n"
               "base_url = \"" + base_url + "\"\n");

    const std::string system_env_body =
        "{\"system_env\":\"AIDEN_CONFIG_TEST_MODEL_API_KEY=stale-file-key\\n"
        "AIDEN_CONFIG_TEST_MODEL_API_KEY=model-system-env-secret\\n\"}";
    REQUIRE(http_request(handle->port, "POST", "/api/system/env", system_env_body).status == 200);

    const std::string test_body =
        "{\"section\":\"model\",\"values\":{\"provider\":\"env-openai\",\"model\":\"gpt-4o\"}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    REQUIRE(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(log_path, "env:AIDEN_CONFIG_TEST_MODEL_API_KEY=", 1000));
    const std::string log = read_file(log_path);
    CHECK(log.find("env:AIDEN_CONFIG_TEST_MODEL_API_KEY=model-system-env-secret") != std::string::npos);
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
}

TEST_CASE("config_web: config test fails closed when system env is invalid or oversized") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    HeadProbeServer probe;
    const std::string base_url = "http://127.0.0.1:" + std::to_string(probe.port());
    write_file(tmp + "/config.json",
               "{\"model_providers\":{\"env-openai\":{\"type\":\"openai\","
               "\"has_api_key\":true,\"base_url\":\"" + base_url + "\"}},"
               "\"model\":{\"provider\":\"env-openai\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_CONFIG_TEST_INVALID_ENV_KEY", "stale-process-env-key");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[model_providers.env-openai]\n"
               "type = \"openai\"\n"
               "api_key = \"$AIDEN_CONFIG_TEST_INVALID_ENV_KEY\"\n"
               "base_url = \"" + base_url + "\"\n");
    write_file(handle->tmp_dir + "/system_env", "AIDEN_CONFIG_TEST_INVALID_ENV_KEY='unterminated\n");

    const std::string test_body =
        "{\"section\":\"model\",\"values\":{\"provider\":\"env-openai\",\"model\":\"gpt-4o\"}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);

    CHECK(test_resp.status == 503);
    CHECK(test_resp.body.find("system env file is invalid") != std::string::npos);
    CHECK(probe.requests().empty());

    write_file(handle->tmp_dir + "/system_env", std::string(65537, 'X'));
    HttpResponse oversized_resp =
        http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(oversized_resp.status == 503);
    CHECK(oversized_resp.body.find("system env file is unavailable") != std::string::npos);
    CHECK(probe.requests().empty());
}

TEST_CASE("config_web: tts config test leaves provider references for agent") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"fish\":{\"provider\":\"fish-audio\","
               "\"api_key\":\"sk-fish-secret-1234\",\"reference_id\":\"ref-abc\"}},"
               "\"tts\":{\"provider\":\"fish\",\"speed\":1},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", tmp + "/config-test.log");
    auto handle = start_server(env);

    // What the slimmed form posts: the reference and the global speed only.
    const std::string test_body =
        "{\"section\":\"tts\",\"values\":{\"provider\":\"fish\",\"speed\":1}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(tmp + "/config-test.log", "stdin:", 1000));
    const std::string log = read_file(tmp + "/config-test.log");
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
    CHECK(log.find("sk-fish-secret-1234") == std::string::npos);
    CHECK(log.find("ref-abc") == std::string::npos);
}

TEST_CASE("config_web: tts config test exposes process env to agent") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string log_path = tmp + "/config-test.log";
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"env-fish\":{\"type\":\"fish-audio\","
               "\"has_api_key\":true,\"reference_id\":\"ref-abc\"}},"
               "\"tts\":{\"provider\":\"env-fish\",\"speed\":1},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_ENV_KEY", "AIDEN_CONFIG_TEST_TTS_API_KEY");
    env.set("AIDEN_CONFIG_TEST_TTS_API_KEY", "tts-process-env-secret");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[tts_providers.env-fish]\n"
               "type = \"fish-audio\"\n"
               "api_key = \"$AIDEN_CONFIG_TEST_TTS_API_KEY\"\n"
               "reference_id = \"ref-abc\"\n");

    const std::string test_body =
        "{\"section\":\"tts\",\"values\":{\"provider\":\"env-fish\",\"speed\":1}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    REQUIRE(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(log_path, "stdin:", 1000));

    const std::string log = read_file(log_path);
    CHECK(log.find("env:AIDEN_CONFIG_TEST_TTS_API_KEY=tts-process-env-secret") != std::string::npos);
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
    CHECK(log.find("\"api_key\"") == std::string::npos);
}

TEST_CASE("config_web: stt config test leaves provider references for agent") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"stt_providers\":{\"tencent-main\":{\"provider\":\"tencent-asr\","
               "\"app_id\":\"1234\",\"secret_id\":\"AKID-xxx\",\"secret_key\":\"secret-yyy\","
               "\"region\":\"ap-shanghai\"}},"
               "\"stt\":{\"provider\":\"tencent-main\",\"language\":\"zh\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", tmp + "/config-test.log");
    auto handle = start_server(env);

    const std::string test_body =
        "{\"section\":\"stt\",\"values\":{\"provider\":\"tencent-main\",\"language\":\"zh\"}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    CHECK(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(tmp + "/config-test.log", "stdin:", 1000));
    const std::string log = read_file(tmp + "/config-test.log");
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
    CHECK(log.find("AKID-xxx") == std::string::npos);
    CHECK(log.find("secret-yyy") == std::string::npos);
}

TEST_CASE("config_web: stt config test honors an empty provider api_key in system env") {
    const std::string tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    const std::string log_path = tmp + "/config-test.log";
    const std::string base_url = "https://stt.example.com/v1";
    write_file(tmp + "/config.json",
               "{\"stt_providers\":{\"env-whisper\":{\"type\":\"openai-whisper\","
               "\"has_api_key\":true,\"model\":\"whisper-1\","
               "\"base_url\":\"" + base_url + "\"}},"
               "\"stt\":{\"provider\":\"env-whisper\",\"language\":\"en\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");

    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_LOG", log_path);
    env.set("AIDEN_AGENT_STUB_CONFIG_TEST_ENV_KEY", "AIDEN_CONFIG_TEST_MISSING_KEY");
    env.set("AIDEN_CONFIG_TEST_MISSING_KEY", "process-env-key-must-not-win");
    auto handle = start_server(env);
    write_file(handle->tmp_dir + "/agent.toml",
               "[stt_providers.env-whisper]\n"
               "type = \"openai-whisper\"\n"
               "api_key = \"$AIDEN_CONFIG_TEST_MISSING_KEY\"\n"
               "model = \"whisper-1\"\n"
               "base_url = \"" + base_url + "\"\n");

    const std::string system_env_body =
        "{\"system_env\":\"AIDEN_CONFIG_TEST_MISSING_KEY=\\n\"}";
    REQUIRE(http_request(handle->port, "POST", "/api/system/env", system_env_body).status == 200);

    const std::string test_body =
        "{\"section\":\"stt\",\"values\":{\"provider\":\"env-whisper\",\"language\":\"en\"}}";
    HttpResponse test_resp = http_request(handle->port, "POST", "/api/config/test", test_body);
    REQUIRE(test_resp.status == 200);
    REQUIRE(wait_for_file_contains(log_path, "env:AIDEN_CONFIG_TEST_MISSING_KEY=", 1000));
    const std::string log = read_file(log_path);
    CHECK(log.find("env:AIDEN_CONFIG_TEST_MISSING_KEY=\n") != std::string::npos);
    CHECK(log.find("stdin:\n" + test_body) != std::string::npos);
}

TEST_CASE("config_web: GET /api/config returns voice providers from the resolved config") {
    StubEnv env;
    const std::string tmp = make_temp_dir();
    write_file(tmp + "/config.json",
               "{\"tts_providers\":{\"fish\":{\"provider\":\"fish-audio\","
               "\"api_key\":\"$FISH_KEY\",\"reference_id\":\"ref-abc\"}},"
               "\"stt_providers\":{\"tencent\":{\"provider\":\"tencent-asr\",\"app_id\":\"1234\","
               "\"region\":\"ap-shanghai\"}},"
               "\"tts\":{\"provider\":\"fish\",\"speed\":1.2},"
               "\"stt\":{\"provider\":\"tencent\",\"language\":\"zh\"},"
               "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"gpt-4o\","
               "\"temperature\":0.2,\"max_response_tokens\":1000,"
               "\"context_window\":0,\"model_max_output_tokens\":0},"
               "\"hid\":{\"pointer_mode\":\"absolute\"},"
               "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}}");
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);

    HttpResponse resp = http_request(handle->port, "GET", "/api/config", "");
    REQUIRE(resp.status == 200);
    CHECK(resp.body.find("\"tts_providers\"") != std::string::npos);
    CHECK(resp.body.find("FISH_KEY") == std::string::npos);
    CHECK(resp.body.find("\"token_env\"") == std::string::npos);
    CHECK(resp.body.find("\"has_api_key\":true") != std::string::npos);
    CHECK(resp.body.find("\"reference_id\":\"ref-abc\"") != std::string::npos);
    CHECK(resp.body.find("\"stt_providers\"") != std::string::npos);
    CHECK(resp.body.find("\"app_id\":\"1234\"") != std::string::npos);
    CHECK(resp.body.find("\"region\":\"ap-shanghai\"") != std::string::npos);
}
