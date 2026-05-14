#include "audio_service_client.h"
#include <chrono>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <string>

static void usage(const char* program) {
    fprintf(stderr,
            "Usage: %s [--socket PATH] <command>\n"
            "\n"
            "Commands:\n"
            "  health                    Print service health\n"
            "  record-stream [--seconds N]  Capture PCM and write to stdout\n"
            "  play-stream [--rate N] [--ch N] [--bits N]  Read PCM from stdin and play\n",
            program);
}

static const char* default_socket() {
    const char* env = getenv("AUDIO_SERVICE_SOCKET");
    return (env && env[0] != '\0') ? env : "/run/audio_service/audio_service.sock";
}

static int cmd_health(aiden::AudioServiceClient& client) {
    aiden::AudioHealthResult h;
    aiden::AidenServiceStatus s = client.health(&h);
    if (s != aiden::AidenServiceStatus::OK) {
        fprintf(stderr, "health failed: %s\n", service_status_to_string(s));
        return 1;
    }
    printf("recording_active=%s playback_active=%s record_sessions=%u playback_sessions=%u\n",
           h.recording_active ? "true" : "false",
           h.playback_active  ? "true" : "false",
           h.record_sessions,
           h.playback_sessions);
    return 0;
}

static int cmd_record_stream(aiden::AudioServiceClient& client, int seconds) {
    aiden::AudioFormat fmt;
    aiden::RecordStartResult rs;
    if (client.start_recording(fmt, &rs) != aiden::AidenServiceStatus::OK) {
        fprintf(stderr, "start_recording failed\n");
        return 1;
    }

    // Use a wall-clock deadline rather than a byte count: chunk sizes from
    // audio_service are not fixed, so byte counting can terminate early.
    auto deadline = std::chrono::steady_clock::now() +
                    std::chrono::seconds(seconds);
    bool read_failed = false;

    while (std::chrono::steady_clock::now() < deadline) {
        aiden::AudioChunkResult chunk;
        aiden::AidenServiceStatus s = client.read_record_chunk(rs.session_id, 2000, &chunk);
        if (s == aiden::AidenServiceStatus::TIMEOUT) continue;
        if (s != aiden::AidenServiceStatus::OK) {
            fprintf(stderr, "read_record_chunk failed: %s\n", service_status_to_string(s));
            read_failed = true;
            break;
        }
        if (chunk.end_of_stream) break;
        if (!chunk.pcm.empty()) {
            fwrite(chunk.pcm.data(), 1, chunk.pcm.size(), stdout);
        }
    }

    client.stop_recording(rs.session_id);
    return read_failed ? 1 : 0;
}

static int cmd_play_stream(aiden::AudioServiceClient& client, const aiden::AudioFormat& fmt) {
    aiden::PlaybackStartResult ps;
    if (client.start_playback(fmt, &ps) != aiden::AidenServiceStatus::OK) {
        fprintf(stderr, "start_playback failed\n");
        return 1;
    }

    uint8_t buf[4096];
    bool write_failed = false;
    for (;;) {
        size_t n = fread(buf, 1, sizeof(buf), stdin);
        if (n > 0) {
            aiden::AidenServiceStatus ws =
                client.write_play_chunk(ps.session_id, buf, n, false);
            if (ws != aiden::AidenServiceStatus::OK) {
                fprintf(stderr, "write_play_chunk failed: %s\n", service_status_to_string(ws));
                write_failed = true;
                break;
            }
        }
        if (n < sizeof(buf)) {
            if (ferror(stdin)) {
                fprintf(stderr, "read stdin failed\n");
                write_failed = true;
            }
            break;
        }
    }

    // Always finalize playback explicitly, even when stdin size is an exact
    // multiple of chunk size.
    aiden::AidenServiceStatus final_ws =
        client.write_play_chunk(ps.session_id, nullptr, 0, true);
    if (final_ws != aiden::AidenServiceStatus::OK) {
        fprintf(stderr, "final write_play_chunk failed: %s\n",
                service_status_to_string(final_ws));
        write_failed = true;
    }

    return write_failed ? 1 : 0;
}

int main(int argc, char** argv) {
    std::string socket_path = default_socket();
    int seconds = 3;
    aiden::AudioFormat play_fmt;

    int i = 1;
    // Parse global --socket option.
    if (i < argc && strcmp(argv[i], "--socket") == 0 && i + 1 < argc) {
        socket_path = argv[++i];
        ++i;
    }

    if (i >= argc) { usage(argv[0]); return 2; }

    std::string cmd = argv[i++];
    aiden::AudioServiceClient client(socket_path.c_str());

    if (cmd == "health") {
        return cmd_health(client);
    }

    if (cmd == "record-stream") {
        for (; i < argc; ++i) {
            if (strcmp(argv[i], "--seconds") == 0 && i + 1 < argc) {
                seconds = atoi(argv[++i]);
            }
        }
        return cmd_record_stream(client, seconds);
    }

    if (cmd == "play-stream") {
        for (; i < argc; ++i) {
            if (strcmp(argv[i], "--rate") == 0 && i + 1 < argc) {
                play_fmt.sample_rate = static_cast<uint32_t>(atoi(argv[++i]));
            } else if (strcmp(argv[i], "--ch") == 0 && i + 1 < argc) {
                play_fmt.channels = static_cast<uint32_t>(atoi(argv[++i]));
            } else if (strcmp(argv[i], "--bits") == 0 && i + 1 < argc) {
                play_fmt.bit_width = static_cast<uint32_t>(atoi(argv[++i]));
            }
        }
        return cmd_play_stream(client, play_fmt);
    }

    fprintf(stderr, "Unknown command: %s\n", cmd.c_str());
    usage(argv[0]);
    return 2;
}
