#include "audio_service_server.h"
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string>
#include <thread>
#include <chrono>
#include <unistd.h>

namespace {

volatile sig_atomic_t g_quit = 0;

void signal_handler(int) {
    g_quit = 1;
}

struct Options {
    std::string socket_path = "/run/audio_service/audio_service.sock";
};

void usage(const char* program) {
    fprintf(stderr, "Usage: %s [--socket PATH]\n", program);
}

bool parse_options(int argc, char** argv, Options* opts) {
    const char* env_socket = getenv("AUDIO_SERVICE_SOCKET");
    if (env_socket && env_socket[0] != '\0') {
        opts->socket_path = env_socket;
    }

    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        if (arg == "--socket" && i + 1 < argc) {
            opts->socket_path = argv[++i];
        } else if (arg == "--help") {
            usage(argv[0]);
            exit(0);
        } else {
            return false;
        }
    }
    return true;
}

}  // namespace

int main(int argc, char** argv) {
    Options opts;
    if (!parse_options(argc, argv, &opts)) {
        usage(argv[0]);
        return 2;
    }

    signal(SIGINT,  signal_handler);
    signal(SIGTERM, signal_handler);

    aiden::AudioServiceServer server(opts.socket_path.c_str());
    if (server.start() != aiden::AidenServiceStatus::OK) {
        fprintf(stderr, "[audio_service] Failed to start socket at %s\n",
                opts.socket_path.c_str());
        return 1;
    }

    printf("[audio_service] Listening on %s\n", opts.socket_path.c_str());

    while (!g_quit) {
        std::this_thread::sleep_for(std::chrono::milliseconds(200));
    }

    server.stop();
    printf("[audio_service] Stopped.\n");
    return 0;
}
