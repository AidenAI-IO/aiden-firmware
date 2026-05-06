#include "minimax_tts.h"
#include "http_client.h"
#include "minimax_codec.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <stdlib.h>
#include <errno.h>
#include <sys/wait.h>
#include <vector>

namespace aiden {

struct StreamContext {
    aiden::AudioPlayer* player;
    minimax::StreamParser parser;
    std::vector<uint8_t> previous_pcm;
};

static bool decode_mp3_to_pcm(const std::vector<uint8_t>& mp3, std::vector<uint8_t>& pcm) {
    int stdin_pipe[2], stdout_pipe[2];
    if (pipe(stdin_pipe) < 0 || pipe(stdout_pipe) < 0) {
        fprintf(stderr, "[error] Failed to create decode pipes\n");
        return false;
    }

    pid_t pid = fork();
    if (pid < 0) {
        fprintf(stderr, "[error] Failed to fork ffmpeg\n");
        close(stdin_pipe[0]);
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);
        close(stdout_pipe[1]);
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
               "-loglevel", "error",
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

    size_t offset = 0;
    while (offset < mp3.size()) {
        ssize_t n = write(stdin_pipe[1], mp3.data() + offset, mp3.size() - offset);
        if (n > 0) {
            offset += (size_t)n;
            continue;
        }
        if (n < 0 && errno == EINTR)
            continue;
        fprintf(stderr, "[error] Failed to write mp3 to ffmpeg: %s\n", strerror(errno));
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);
        waitpid(pid, NULL, 0);
        return false;
    }
    close(stdin_pipe[1]);

    pcm.clear();
    uint8_t buf[4096];
    while (true) {
        ssize_t n = read(stdout_pipe[0], buf, sizeof(buf));
        if (n > 0) {
            pcm.insert(pcm.end(), buf, buf + n);
            continue;
        }
        if (n < 0 && errno == EINTR)
            continue;
        break;
    }
    close(stdout_pipe[0]);

    int status = 0;
    waitpid(pid, &status, 0);
    return WIFEXITED(status) && WEXITSTATUS(status) == 0 && !pcm.empty();
}

static void stream_chunk_callback(const char* data, size_t len, void* user_data) {
    StreamContext* ctx = (StreamContext*)user_data;
    std::vector<std::vector<uint8_t>> snapshots = ctx->parser.feed(data, len);

    for (size_t i = 0; i < snapshots.size(); i++) {
        std::vector<uint8_t> pcm;
        if (!decode_mp3_to_pcm(snapshots[i], pcm)) {
            fprintf(stderr, "[error] Failed to decode streamed MiniMax audio snapshot\n");
            continue;
        }

        size_t prefix = 0;
        while (prefix < ctx->previous_pcm.size() &&
               prefix < pcm.size() &&
               ctx->previous_pcm[prefix] == pcm[prefix]) {
            prefix++;
        }

        if (prefix < pcm.size()) {
            ctx->player->play(pcm.data() + prefix, (uint32_t)(pcm.size() - prefix));
        }

        ctx->previous_pcm = std::move(pcm);
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

    StreamContext ctx;
    ctx.player = &player;

    HttpClient http;
    bool success = http.post_stream(
        "https://api.minimaxi.com/v1/t2a_v2",
        api_key_.c_str(),
        request_str,
        stream_chunk_callback,
        &ctx);
    free(request_str);

    if (!success) {
        fprintf(stderr, "[error] MiniMax TTS stream failed\n");
        return false;
    }

    fprintf(stderr, "[minimax] TTS stream complete\n");
    return true;
}

}
