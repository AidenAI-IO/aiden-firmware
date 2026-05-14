#include "minimax_tts.h"
#include "http_client.h"
#include "minimax_codec.h"
#include "service_status.h"
#include "cJSON/cJSON.h"
#include <errno.h>
#include <atomic>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <unistd.h>
#include <vector>

namespace aiden {

struct StreamContext {
    int ffmpeg_stdin;
    int ffmpeg_stdout;
    pid_t ffmpeg_pid;
    AudioServiceClient* audio;
    uint64_t playback_session_id;
    pthread_t reader_thread;
    minimax::StreamParser parser;
    std::atomic<bool> write_failed{false};
    size_t sent_chunks = 0;
    size_t sent_bytes = 0;
};

static void* pcm_reader_thread(void* arg) {
    StreamContext* ctx = static_cast<StreamContext*>(arg);

    uint8_t buf[4096];
    for (;;) {
        ssize_t n = read(ctx->ffmpeg_stdout, buf, sizeof(buf));
        if (n > 0) {
            AidenServiceStatus ws = ctx->audio->write_play_chunk(
                ctx->playback_session_id, buf, static_cast<size_t>(n), false);
            if (ws != AidenServiceStatus::OK) {
                fprintf(stderr,
                        "[minimax] write_play_chunk failed: status=%s, bytes=%zd\n",
                        service_status_to_string(ws), n);
                ctx->write_failed.store(true);
                break;
            }
            ctx->sent_chunks++;
            ctx->sent_bytes += static_cast<size_t>(n);
            continue;
        }
        if (n < 0 && errno == EINTR) continue;
        break;
    }
    return nullptr;
}

static void stream_chunk_callback(const char* data, size_t len, void* user_data) {
    StreamContext* ctx = static_cast<StreamContext*>(user_data);
    std::vector<std::vector<uint8_t>> chunks = ctx->parser.feed(data, len);
    for (size_t i = 0; i < chunks.size(); ++i) {
        const std::vector<uint8_t>& mp3 = chunks[i];
        if (mp3.empty()) continue;
        size_t offset = 0;
        while (offset < mp3.size()) {
            ssize_t n = write(ctx->ffmpeg_stdin,
                              mp3.data() + offset,
                              mp3.size() - offset);
            if (n > 0) { offset += static_cast<size_t>(n); continue; }
            if (n < 0 && errno == EINTR) continue;
            return;
        }
    }
}

MinimaxTTS::MinimaxTTS(const char* api_key, const char* voice_id,
                       const char* emotion, float speed)
    : api_key_(api_key), voice_id_(voice_id), emotion_(emotion), speed_(speed) {}

bool MinimaxTTS::text_to_speech_stream(const char* text,
                                       aiden::AudioServiceClient& audio) {
    // Build request JSON.
    cJSON* request = cJSON_CreateObject();
    cJSON_AddStringToObject(request, "model", "speech-2.8-hd");
    cJSON_AddStringToObject(request, "text", text);
    cJSON_AddBoolToObject(request, "stream", true);

    cJSON* voice_setting = cJSON_CreateObject();
    cJSON_AddStringToObject(voice_setting, "voice_id", voice_id_.c_str());
    cJSON_AddNumberToObject(voice_setting, "speed", speed_);
    cJSON_AddNumberToObject(voice_setting, "vol", 1.0);
    cJSON_AddNumberToObject(voice_setting, "pitch", 0);
    cJSON_AddStringToObject(voice_setting, "emotion", emotion_.c_str());
    cJSON_AddItemToObject(request, "voice_setting", voice_setting);

    cJSON* audio_setting = cJSON_CreateObject();
    cJSON_AddNumberToObject(audio_setting, "sample_rate", 32000);
    cJSON_AddNumberToObject(audio_setting, "bitrate", 128000);
    cJSON_AddStringToObject(audio_setting, "format", "mp3");
    cJSON_AddNumberToObject(audio_setting, "channel", 1);
    cJSON_AddItemToObject(request, "audio_setting", audio_setting);

    cJSON* stream_options = cJSON_CreateObject();
    cJSON_AddBoolToObject(stream_options, "exclude_aggregated_audio", true);
    cJSON_AddItemToObject(request, "stream_options", stream_options);
    cJSON_AddBoolToObject(request, "subtitle_enable", false);

    char* request_str = cJSON_PrintUnformatted(request);
    fprintf(stderr, "[minimax] Request: %s\n", request_str);
    cJSON_Delete(request);

    // Open a playback session on audio_service (16kHz/16bit/mono — ffmpeg output).
    AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels    = 1;
    fmt.bit_width   = 16;

    PlaybackStartResult playback;
    if (audio.start_playback(fmt, &playback) != AidenServiceStatus::OK) {
        fprintf(stderr, "[minimax] Failed to open playback session\n");
        free(request_str);
        return false;
    }

    // Spawn ffmpeg: mp3 stdin → pcm s16le 16kHz mono stdout.
    int stdin_pipe[2] = {-1, -1};
    int stdout_pipe[2] = {-1, -1};
    if (pipe(stdin_pipe) < 0 || pipe(stdout_pipe) < 0) {
        fprintf(stderr, "[error] Failed to create pipes\n");
        if (stdin_pipe[0] >= 0) {
            close(stdin_pipe[0]);
            close(stdin_pipe[1]);
        }
        if (stdout_pipe[0] >= 0) {
            close(stdout_pipe[0]);
            close(stdout_pipe[1]);
        }
        audio.stop_playback(playback.session_id);
        free(request_str);
        return false;
    }

    pid_t pid = fork();
    if (pid < 0) {
        fprintf(stderr, "[error] Failed to fork\n");
        close(stdin_pipe[0]);
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);
        close(stdout_pipe[1]);
        audio.stop_playback(playback.session_id);
        free(request_str);
        return false;
    }

    if (pid == 0) {
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);
        dup2(stdin_pipe[0], STDIN_FILENO);
        dup2(stdout_pipe[1], STDOUT_FILENO);
        close(stdin_pipe[0]);
        close(stdout_pipe[1]);
        execlp("ffmpeg", "ffmpeg",
               "-i", "pipe:0",
               "-f", "s16le",
               "-ar", "16000",
               "-ac", "1",
               "pipe:1",
               nullptr);
        _exit(1);
    }

    close(stdin_pipe[0]);
    close(stdout_pipe[1]);

    StreamContext ctx;
    ctx.ffmpeg_stdin       = stdin_pipe[1];
    ctx.ffmpeg_stdout      = stdout_pipe[0];
    ctx.ffmpeg_pid         = pid;
    ctx.audio              = &audio;
    ctx.playback_session_id = playback.session_id;

    pthread_create(&ctx.reader_thread, nullptr, pcm_reader_thread, &ctx);

    HttpClient http;
    bool success = http.post_stream(
        "https://api.minimaxi.com/v1/t2a_v2",
        api_key_.c_str(),
        request_str,
        stream_chunk_callback,
        &ctx);
    free(request_str);

    close(ctx.ffmpeg_stdin);
    pthread_join(ctx.reader_thread, nullptr);
    close(ctx.ffmpeg_stdout);

    int status;
    if (waitpid(ctx.ffmpeg_pid, &status, 0) < 0) {
        fprintf(stderr, "[minimax] waitpid failed: %s\n", strerror(errno));
        success = false;
    } else if (!WIFEXITED(status)) {
        fprintf(stderr, "[minimax] ffmpeg exited abnormally: status=%d\n", status);
        success = false;
    } else if (WEXITSTATUS(status) != 0) {
        fprintf(stderr, "[minimax] ffmpeg exited with code %d\n", WEXITSTATUS(status));
        success = false;
    }

    // Pad a short silence tail before finalizing playback to avoid clipping
    // the last phonemes on some AO drivers that stop slightly early.
    {
        const size_t kTailMs = 200;
        const size_t bytes_per_sample = 2;  // s16le
        const size_t tail_bytes = (16000 * kTailMs / 1000) * bytes_per_sample;
        std::vector<uint8_t> tail_silence(tail_bytes, 0);
        AidenServiceStatus pad_ws = audio.write_play_chunk(
            playback.session_id, tail_silence.data(), tail_silence.size(), false);
        if (pad_ws != AidenServiceStatus::OK) {
            fprintf(stderr, "[minimax] silence tail write failed: status=%s\n",
                    service_status_to_string(pad_ws));
        }
    }

    // Always signal end-of-stream so the playback session drains and closes
    // cleanly, even if the HTTP stream failed partway through. Without this,
    // the session would stay open until stop_playback() is called explicitly.
    AidenServiceStatus final_ws =
        audio.write_play_chunk(playback.session_id, nullptr, 0, true);
    if (final_ws != AidenServiceStatus::OK) {
        fprintf(stderr, "[minimax] final write_play_chunk failed: status=%s\n",
                service_status_to_string(final_ws));
        success = false;
    }

    if (ctx.write_failed.load()) {
        fprintf(stderr,
                "[minimax] TTS playback stream truncated: sent_chunks=%zu sent_bytes=%zu\n",
                ctx.sent_chunks, ctx.sent_bytes);
        success = false;
    }

    if (!success) {
        fprintf(stderr, "[error] MiniMax TTS stream failed\n");
        return false;
    }

    fprintf(stderr, "[minimax] TTS stream complete (chunks=%zu bytes=%zu)\n",
            ctx.sent_chunks, ctx.sent_bytes);
    return true;
}

}  // namespace aiden
