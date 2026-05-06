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

struct StreamDecoder {
    int ffmpeg_stdin;
    int ffmpeg_stdout;
    pid_t ffmpeg_pid;
    aiden::AudioPlayer* player;
    pthread_t reader_thread;
    bool reader_running;
};

static void* pcm_reader_thread(void* arg) {
    StreamDecoder* decoder = (StreamDecoder*)arg;
    uint8_t pcm_buf[4096];
    while (decoder->reader_running) {
        ssize_t n = read(decoder->ffmpeg_stdout, pcm_buf, sizeof(pcm_buf));
        if (n <= 0) break;
        decoder->player->play(pcm_buf, n);
    }
    return NULL;
}

static bool start_decoder(StreamDecoder& decoder, aiden::AudioPlayer* player) {
    int stdin_pipe[2], stdout_pipe[2];
    if (pipe(stdin_pipe) < 0 || pipe(stdout_pipe) < 0) {
        fprintf(stderr, "[error] Failed to create decoder pipes\n");
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

    decoder.ffmpeg_stdin = stdin_pipe[1];
    decoder.ffmpeg_stdout = stdout_pipe[0];
    decoder.ffmpeg_pid = pid;
    decoder.player = player;
    decoder.reader_running = true;

    pthread_create(&decoder.reader_thread, NULL, pcm_reader_thread, &decoder);
    return true;
}

static void stop_decoder(StreamDecoder& decoder) {
    close(decoder.ffmpeg_stdin);
    decoder.reader_running = false;
    pthread_join(decoder.reader_thread, NULL);
    close(decoder.ffmpeg_stdout);
    int status;
    waitpid(decoder.ffmpeg_pid, &status, 0);
}

struct StreamContext {
    aiden::AudioPlayer* player;
    minimax::StreamParser parser;
    StreamDecoder decoder;
    bool decoder_started;
};

static void stream_chunk_callback(const char* data, size_t len, void* user_data) {
    StreamContext* ctx = (StreamContext*)user_data;
    std::vector<minimax::StreamChunk> chunks = ctx->parser.feed(data, len);

    for (size_t i = 0; i < chunks.size(); i++) {
        const minimax::StreamChunk& chunk = chunks[i];

        if (chunk.reset_decoder) {
            fprintf(stderr, "[minimax] Reset decoder due to unrelated snapshot\n");
            if (ctx->decoder_started) {
                stop_decoder(ctx->decoder);
            }
            ctx->decoder_started = start_decoder(ctx->decoder, ctx->player);
            if (!ctx->decoder_started) {
                fprintf(stderr, "[error] Failed to restart decoder\n");
                continue;
            }
        }

        if (!ctx->decoder_started) {
            ctx->decoder_started = start_decoder(ctx->decoder, ctx->player);
            if (!ctx->decoder_started) {
                fprintf(stderr, "[error] Failed to start decoder\n");
                continue;
            }
        }

        if (!chunk.audio.empty()) {
            size_t offset = 0;
            while (offset < chunk.audio.size()) {
                ssize_t n = write(ctx->decoder.ffmpeg_stdin,
                                  chunk.audio.data() + offset,
                                  chunk.audio.size() - offset);
                if (n > 0) {
                    offset += (size_t)n;
                    continue;
                }
                if (n < 0 && errno == EINTR)
                    continue;
                fprintf(stderr, "[error] Failed to write mp3 delta to decoder: %s\n", strerror(errno));
                break;
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

    StreamContext ctx;
    ctx.player = &player;
    ctx.decoder_started = false;

    HttpClient http;
    bool success = http.post_stream(
        "https://api.minimaxi.com/v1/t2a_v2",
        api_key_.c_str(),
        request_str,
        stream_chunk_callback,
        &ctx);
    free(request_str);

    if (ctx.decoder_started) {
        stop_decoder(ctx.decoder);
    }

    if (!success) {
        fprintf(stderr, "[error] MiniMax TTS stream failed\n");
        return false;
    }

    fprintf(stderr, "[minimax] TTS stream complete\n");
    return true;
}

}
