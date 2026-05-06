#include "minimax_tts.h"
#include "http_client.h"
#include "minimax_codec.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <stdlib.h>
#include <errno.h>
#include <pthread.h>
#include <sys/wait.h>
#include <vector>

namespace aiden {

struct StreamContext {
    int ffmpeg_stdin;
    int ffmpeg_stdout;
    pid_t ffmpeg_pid;
    aiden::AudioPlayer* player;
    pthread_t reader_thread;
    bool reader_running;
    minimax::StreamParser parser;
};

static void* pcm_reader_thread(void* arg) {
    StreamContext* ctx = (StreamContext*)arg;

    uint8_t pcm_buf[4096];
    while (ctx->reader_running) {
        ssize_t n = read(ctx->ffmpeg_stdout, pcm_buf, sizeof(pcm_buf));
        if (n <= 0) break;

        ctx->player->play(pcm_buf, n);
    }

    return NULL;
}

static void stream_chunk_callback(const char* data, size_t len, void* user_data) {
    StreamContext* ctx = (StreamContext*)user_data;
    std::vector<std::vector<uint8_t>> chunks = ctx->parser.feed(data, len);
    for (size_t i = 0; i < chunks.size(); i++) {
        const std::vector<uint8_t>& mp3_chunk = chunks[i];
        if (!mp3_chunk.empty()) {
            size_t offset = 0;
            while (offset < mp3_chunk.size()) {
                ssize_t n = write(ctx->ffmpeg_stdin,
                                  mp3_chunk.data() + offset,
                                  mp3_chunk.size() - offset);
                if (n > 0) {
                    offset += (size_t)n;
                    continue;
                }
                if (n < 0 && errno == EINTR)
                    continue;
                return;
            }
        }
    }
}

MinimaxTTS::MinimaxTTS(const char* api_key, const char* voice_id,
                       const char* emotion, float speed)
    : api_key_(api_key), voice_id_(voice_id), emotion_(emotion), speed_(speed) {
}

bool MinimaxTTS::text_to_speech_stream(const char* text, aiden::AudioPlayer& player) {
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

    cJSON_AddBoolToObject(request, "subtitle_enable", false);

    char* request_str = cJSON_PrintUnformatted(request);
    fprintf(stderr, "[minimax] Request: %s\n", request_str);
    cJSON_Delete(request);

    int stdin_pipe[2], stdout_pipe[2];
    if (pipe(stdin_pipe) < 0 || pipe(stdout_pipe) < 0) {
        fprintf(stderr, "[error] Failed to create pipes\n");
        free(request_str);
        return false;
    }

    pid_t pid = fork();
    if (pid < 0) {
        fprintf(stderr, "[error] Failed to fork\n");
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
               NULL);
        _exit(1);
    }

    close(stdin_pipe[0]);
    close(stdout_pipe[1]);

    StreamContext ctx;
    ctx.ffmpeg_stdin = stdin_pipe[1];
    ctx.ffmpeg_stdout = stdout_pipe[0];
    ctx.ffmpeg_pid = pid;
    ctx.player = &player;
    ctx.reader_running = true;

    pthread_create(&ctx.reader_thread, NULL, pcm_reader_thread, &ctx);

    HttpClient http;
    bool success = http.post_stream(
        "https://api.minimaxi.com/v1/t2a_v2",
        api_key_.c_str(),
        request_str,
        stream_chunk_callback,
        &ctx);
    free(request_str);

    close(ctx.ffmpeg_stdin);

    ctx.reader_running = false;
    pthread_join(ctx.reader_thread, NULL);

    close(ctx.ffmpeg_stdout);
    int status;
    waitpid(ctx.ffmpeg_pid, &status, 0);

    if (!success) {
        fprintf(stderr, "[error] MiniMax TTS stream failed\n");
        return false;
    }

    fprintf(stderr, "[minimax] TTS stream complete\n");
    return true;
}

}
