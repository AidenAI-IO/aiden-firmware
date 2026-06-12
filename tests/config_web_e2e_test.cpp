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

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <memory>
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

// Captures one HTTP response. Status code is parsed from the status line; the
// body is the bytes after the first blank line. We don't care about
// chunked/keep-alive since config_web closes the connection per response.
struct HttpResponse {
    int status = 0;
    std::string body;
};

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

    void set(const std::string& key, const std::string& value) {
        if (key == "AIDEN_AGENT_BIN") has_agent_bin = true;
        entries.push_back(key + "=" + value);
    }
};

std::unique_ptr<ServerHandle> start_server(const StubEnv& stub_env) {
    auto handle = std::unique_ptr<ServerHandle>(new ServerHandle());
    handle->tmp_dir = make_temp_dir();
    handle->port = pick_ephemeral_port();

    const std::string agent_toml_path = handle->tmp_dir + "/agent.toml";
    const std::string wifi_conf_path = handle->tmp_dir + "/wifi.conf";
    const std::string sysenv_path = handle->tmp_dir + "/system_env";
    write_file(agent_toml_path, "");
    write_file(wifi_conf_path, "");
    write_file(sysenv_path, "");

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
        std::vector<char*> argv = {
            const_cast<char*>(AIDEN_CONFIG_WEB_BIN),
            const_cast<char*>("--bind=127.0.0.1"),
            const_cast<char*>(port_arg.c_str()),
            const_cast<char*>(config_arg.c_str()),
            const_cast<char*>(wifi_arg.c_str()),
            const_cast<char*>(sysenv_arg.c_str()),
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

    cJSON* model = cJSON_GetObjectItem(config, "model");
    REQUIRE(model != nullptr);
    cJSON* provider = cJSON_GetObjectItem(model, "provider");
    REQUIRE(provider != nullptr);
    REQUIRE(provider->valuestring != nullptr);
    CHECK(std::string(provider->valuestring) == "openrouter");
    cJSON_Delete(parsed);
}

TEST_CASE("config_web: GET /api/config reports invalid resolved config schema") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/config.json", "{\"model\":{}}\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CONFIG_FILE", tmp + "/config.json");
    auto handle = start_server(env);
    HttpResponse resp = http_request(handle->port, "GET", "/api/config");
    CHECK(resp.status == 200);
    CHECK(resp.body.find("agent config missing required fields") != std::string::npos);

    cJSON* parsed = cJSON_Parse(resp.body.c_str());
    REQUIRE(parsed != nullptr);
    cJSON* config_error = cJSON_GetObjectItem(parsed, "config_error");
    REQUIRE(config_error != nullptr);
    REQUIRE(config_error->valuestring != nullptr);
    CHECK(std::string(config_error->valuestring) == "agent config missing required fields");
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

TEST_CASE("config_web: POST /api/config returns 200 when stub config-check approves") {
    StubEnv env;  // defaults: check returns valid:true
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"model\":{\"provider\":\"openai\",\"model\":\"x\",\"api_key\":\"k\"},"
        "\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 200);
}

TEST_CASE("config_web: POST /api/config returns 400 when stub config-check rejects with reasons") {
    auto tmp = make_temp_dir();
    auto cleanup = std::unique_ptr<void, void(*)(void*)>(
        const_cast<char*>(tmp.c_str()),
        [](void* p) { std::string cmd = std::string("rm -rf '") + (char*)p + "'"; (void)std::system(cmd.c_str()); }
    );
    write_file(tmp + "/check.json",
               "{\"valid\":false,\"errors\":[{\"field\":\"x\",\"message\":\"stub-rejected\"}]}\n");
    StubEnv env;
    env.set("AIDEN_AGENT_STUB_CHECK_FILE", tmp + "/check.json");
    env.set("AIDEN_AGENT_STUB_CHECK_EXIT", "1");
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 400);
    CHECK(resp.body.find("stub-rejected") != std::string::npos);
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
    env.set("AIDEN_AGENT_STUB_CHECK_FILE", tmp + "/check.txt");
    env.set("AIDEN_AGENT_STUB_CHECK_EXIT", "1");
    auto handle = start_server(env);
    const std::string body =
        "{\"config\":{\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse resp = http_request(handle->port, "POST", "/api/config", body);
    CHECK(resp.status == 503);
    CHECK(resp.body.find("unexpected response") != std::string::npos);
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
        "{\"config\":{\"hid\":{\"pointer_mode\":\"absolute\"},"
        "\"search\":{\"provider\":\"duckduckgo\"},\"agent\":{}},\"apply_wifi\":false}";
    HttpResponse save = http_request(handle->port, "POST", "/api/config", body);
    CHECK(save.status == 503);
    CHECK(save.body.find("agent binary not found") != std::string::npos);
}

TEST_CASE("config_web: failed manual OTA update releases launch lock even if a daemon keeps running") {
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
    REQUIRE(wait_for_file_contains(log_path, "[config_web] ota update exited rc=1", 2000));

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
