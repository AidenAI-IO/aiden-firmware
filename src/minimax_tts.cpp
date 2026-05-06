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
};

static void* pcm_reader_thread(void* arg) {
    StreamContext* ctx = (StreamContext*)arg;

    uint8_t pcm_buf[4096];
    while (true) {
        ssize_t n = read(ctx->ffmpeg_stdout, pcm_buf, sizeof(pcm_buf));
        if (n <= 0) break;

        ctx->player->play(pcm_buf, n);
    }

    return NULL;
}

MinimaxTTS::MinimaxTTS(const char* api_key, const char* voice_id,
                       const char* emotion, float speed)
    : api_key_(api_key), voice_id_(voice_id), emotion_(emotion), speed_(speed) {
}

bool MinimaxTTS::text_to_speech_stream(const char* text, aiden::AudioPlayer& player) {
    cJSON* request = cJSON_CreateObject();
    cJSON_AddStringToObject(request, "model", "speech-2.8-hd");
    cJSON_AddStringToObject(request, "text", text);
    cJSON_AddBoolToObject(request, "stream", false);

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

    std::string response;
    HttpClient http;
    bool success = http.post_json(
        "https://api.minimaxi.com/v1/t2a_v2",
        api_key_.c_str(),
        request_str,
        response);
    free(request_str);

    if (!success || response.empty()) {
        fprintf(stderr, "[error] MiniMax TTS request failed or returned empty response\n");
        return false;
    }

    cJSON* json = cJSON_Parse(response.c_str());
    if (!json) {
        fprintf(stderr, "[error] MiniMax TTS returned invalid JSON\n");
        return false;
    }

    cJSON* base_resp = cJSON_GetObjectItem(json, "base_resp");
    if (base_resp) {
        cJSON* status_code = cJSON_GetObjectItem(base_resp, "status_code");
        cJSON* status_msg = cJSON_GetObjectItem(base_resp, "status_msg");
        if (status_code && status_code->valueint != 0) {
            fprintf(stderr, "[error] MiniMax TTS API error: code=%d, msg=%s\n",
                    status_code->valueint,
                    status_msg ? status_msg->valuestring : "unknown");
            cJSON_Delete(json);
            return false;
        }
    }

    cJSON* data_obj = cJSON_GetObjectItem(json, "data");
    cJSON* audio = data_obj ? cJSON_GetObjectItem(data_obj, "audio") : NULL;
    if (!audio || audio->type != cJSON_String || !audio->valuestring || audio->valuestring[0] == '\0') {
        fprintf(stderr, "[error] MiniMax TTS response missing audio payload\n");
        cJSON_Delete(json);
        return false;
    }

    std::vector<uint8_t> mp3 = minimax::hex_decode(audio->valuestring);
    cJSON_Delete(json);

    if (mp3.empty()) {
        fprintf(stderr, "[error] MiniMax TTS audio payload decoded empty\n");
        return false;
    }

    int stdin_pipe[2], stdout_pipe[2];
    if (pipe(stdin_pipe) < 0 || pipe(stdout_pipe) < 0) {
        fprintf(stderr, "[error] Failed to create pipes\n");
        return false;
    }

    pid_t pid = fork();
    if (pid < 0) {
        fprintf(stderr, "[error] Failed to fork\n");
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

    pthread_create(&ctx.reader_thread, NULL, pcm_reader_thread, &ctx);

    size_t offset = 0;
    while (offset < mp3.size()) {
        ssize_t n = write(ctx.ffmpeg_stdin, mp3.data() + offset, mp3.size() - offset);
        if (n > 0) {
            offset += (size_t)n;
            continue;
        }
        if (n < 0 && errno == EINTR)
            continue;
        fprintf(stderr, "[error] Failed to write mp3 to ffmpeg: %s\n", strerror(errno));
        close(ctx.ffmpeg_stdin);
        close(ctx.ffmpeg_stdout);
        pthread_join(ctx.reader_thread, NULL);
        waitpid(ctx.ffmpeg_pid, NULL, 0);
        return false;
    }

    close(ctx.ffmpeg_stdin);

    int status;
    waitpid(ctx.ffmpeg_pid, &status, 0);
    pthread_join(ctx.reader_thread, NULL);
    close(ctx.ffmpeg_stdout);

    fprintf(stderr, "[minimax] TTS complete\n");
    return true;
}

}
