// Minimal `agent` CLI replacement used by config_web E2E tests. Behaviour is
// controlled by environment variables so the test driver can flip a single
// stub between scenarios (valid metadata, broken metadata, valid check,
// rejecting check, hung process, ...) without rebuilding.
//
// Variables:
//   AIDEN_AGENT_STUB_META_FILE   path to a file whose contents are written
//                                verbatim to stdout for `config-meta`. If
//                                unset, prints a minimal valid metadata
//                                document.
//   AIDEN_AGENT_STUB_META_EXIT   integer exit code for `config-meta`
//                                (default 0).
//   AIDEN_AGENT_STUB_CHECK_FILE  path to a file whose contents are written
//                                verbatim to stdout for `config-check`. If
//                                unset, prints {"valid": true, "errors": []}.
//   AIDEN_AGENT_STUB_CHECK_EXIT  integer exit code for `config-check`
//                                (default 0).
//   AIDEN_AGENT_STUB_SLEEP_MS    if set, sleep this many ms before producing
//                                output -- used to exercise the timeout path.
//
// Unknown subcommands exit 99 with empty stdout.

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>
#include <thread>
#include <chrono>

namespace {

const char* kDefaultMeta =
    "{\"sections\":["
    "{\"name\":\"agent\",\"fields\":["
    "{\"key\":\"input_mode\",\"widget\":\"select\","
    "\"enum\":[{\"value\":\"text\"}],\"default\":\"text\"}"
    "]}"
    "]}\n";

const char* kDefaultCheck = "{\"valid\":true,\"errors\":[]}\n";

void maybe_sleep() {
    const char* sleep_ms = std::getenv("AIDEN_AGENT_STUB_SLEEP_MS");
    if (!sleep_ms || sleep_ms[0] == '\0') {
        return;
    }
    int ms = std::atoi(sleep_ms);
    if (ms > 0) {
        std::this_thread::sleep_for(std::chrono::milliseconds(ms));
    }
}

bool write_file_contents(const char* env_var, const char* default_text) {
    const char* path = std::getenv(env_var);
    if (!path || path[0] == '\0') {
        std::fputs(default_text, stdout);
        return true;
    }
    std::ifstream in(path);
    if (!in.good()) {
        std::fprintf(stderr, "stub: failed to open %s=%s\n", env_var, path);
        return false;
    }
    std::ostringstream buf;
    buf << in.rdbuf();
    std::fputs(buf.str().c_str(), stdout);
    return true;
}

int env_int(const char* var, int fallback) {
    const char* val = std::getenv(var);
    if (!val || val[0] == '\0') {
        return fallback;
    }
    return std::atoi(val);
}

}  // namespace

int main(int argc, char** argv) {
    if (argc < 2) {
        return 99;
    }
    const std::string sub = argv[1];

    if (sub == "config-meta") {
        maybe_sleep();
        if (!write_file_contents("AIDEN_AGENT_STUB_META_FILE", kDefaultMeta)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_META_EXIT", 0);
    }

    if (sub == "config-check") {
        // Drain stdin so config_web's writer doesn't see EPIPE before we
        // respond. We don't actually inspect the payload -- the test driver
        // controls what we report via the env file.
        char buf[4096];
        while (std::fread(buf, 1, sizeof(buf), stdin) > 0) {
        }
        maybe_sleep();
        if (!write_file_contents("AIDEN_AGENT_STUB_CHECK_FILE", kDefaultCheck)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_CHECK_EXIT", 0);
    }

    return 99;
}
