#include "aiden_sdk.h"
#include "config.h"
#include "http_client.h"
#include "openrouter_client.h"
#include "minimax_tts.h"
#include "vad.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <stdlib.h>
#include <signal.h>
#include <unistd.h>
#include <string.h>
#include <sys/select.h>
#include <termios.h>
#include <vector>
#include <string>

static volatile bool quit = false;
static volatile bool wakeup_triggered = false;

static void signal_handler(int) { quit = true; }

static bool stdin_has_data() {
    fd_set fds;
    struct timeval tv = {0, 0};
    FD_ZERO(&fds);
    FD_SET(STDIN_FILENO, &fds);
    return select(STDIN_FILENO + 1, &fds, NULL, NULL, &tv) > 0;
}

static void drain_stdin() {
    while (stdin_has_data()) getchar();
}

static void on_wakeup() {
    printf("\n[wakeup] GPIO 33 triggered, starting to listen...\n");
    wakeup_triggered = true;
}

static std::vector<uint8_t> pcm_to_wav(const std::vector<int16_t>& pcm) {
    uint32_t data_size = pcm.size() * 2;
    uint32_t file_size = 36 + data_size;
    std::vector<uint8_t> wav(44 + data_size);
    uint8_t* p = wav.data();

    memcpy(p, "RIFF", 4); p += 4;
    *(uint32_t*)p = file_size; p += 4;
    memcpy(p, "WAVE", 4); p += 4;
    memcpy(p, "fmt ", 4); p += 4;
    *(uint32_t*)p = 16; p += 4;
    *(uint16_t*)p = 1; p += 2;
    *(uint16_t*)p = 1; p += 2;
    *(uint32_t*)p = 16000; p += 4;
    *(uint32_t*)p = 32000; p += 4;
    *(uint16_t*)p = 2; p += 2;
    *(uint16_t*)p = 16; p += 2;
    memcpy(p, "data", 4); p += 4;
    *(uint32_t*)p = data_size; p += 4;
    memcpy(p, pcm.data(), data_size);
    return wav;
}

static std::string execute_tool(const char* hid_binary, const char* tool_name,
                                const char* args_json) {
    cJSON* args = cJSON_Parse(args_json);
    if (!args) {
        fprintf(stderr, "[error] Failed to parse tool arguments JSON\n");
        return "error: invalid JSON";
    }

    char cmd[1024];

    if (strcmp(tool_name, "keyboard_tap") == 0) {
        cJSON* keys = cJSON_GetObjectItem(args, "keys");
        if (!keys || keys->type != cJSON_Array) {
            cJSON_Delete(args);
            return "error: missing keys array";
        }
        int len = snprintf(cmd, sizeof(cmd), "sudo %s keyboard tap", hid_binary);
        int count = cJSON_GetArraySize(keys);
        for (int i = 0; i < count && len < (int)sizeof(cmd) - 32; i++) {
            cJSON* key = cJSON_GetArrayItem(keys, i);
            if (key && key->type == cJSON_String)
                len += snprintf(cmd + len, sizeof(cmd) - len, " %s", key->valuestring);
        }
    }
    else if (strcmp(tool_name, "keyboard_text") == 0) {
        cJSON* text = cJSON_GetObjectItem(args, "text");
        if (!text || text->type != cJSON_String) {
            cJSON_Delete(args);
            return "error: missing text";
        }
        snprintf(cmd, sizeof(cmd), "sudo %s keyboard text '%s'",
                hid_binary, text->valuestring);
    }
    else if (strcmp(tool_name, "touch_click") == 0) {
        cJSON* x = cJSON_GetObjectItem(args, "x");
        cJSON* y = cJSON_GetObjectItem(args, "y");
        if (!x || !y) {
            cJSON_Delete(args);
            return "error: missing x or y";
        }
        snprintf(cmd, sizeof(cmd), "sudo %s touch click %d %d",
                hid_binary, x->valueint, y->valueint);
    }
    else if (strcmp(tool_name, "touch_swipe") == 0) {
        cJSON* x1 = cJSON_GetObjectItem(args, "x1");
        cJSON* y1 = cJSON_GetObjectItem(args, "y1");
        cJSON* x2 = cJSON_GetObjectItem(args, "x2");
        cJSON* y2 = cJSON_GetObjectItem(args, "y2");
        if (!x1 || !y1 || !x2 || !y2) {
            cJSON_Delete(args);
            return "error: missing coordinates";
        }
        snprintf(cmd, sizeof(cmd),
                "sudo %s touch down %d %d && sudo %s touch move %d %d && sudo %s touch up",
                hid_binary, x1->valueint, y1->valueint,
                hid_binary, x2->valueint, y2->valueint,
                hid_binary);
    }
    else {
        cJSON_Delete(args);
        return "error: unknown tool";
    }

    cJSON_Delete(args);

    FILE* fp = popen(cmd, "r");
    if (!fp) return "error: popen failed";

    char buf[256];
    std::string result;
    while (fgets(buf, sizeof(buf), fp))
        result += buf;

    pclose(fp);
    return result.empty() ? "ok" : result;
}

static void process_utterance(const std::vector<int16_t>& utterance,
                              aiden::OpenRouterClient& llm,
                              aiden::MinimaxTTS& tts,
                              aiden::AudioPlayer& player,
                              const aiden::AgentConfig& config) {
    float duration = utterance.size() / 16000.0f;
    printf("[utterance] %.1fs of speech\n", duration);

    std::vector<uint8_t> wav = pcm_to_wav(utterance);
    printf("[debug] WAV size: %zu bytes\n", wav.size());

    while (true) {
        std::string response;
        std::vector<aiden::ToolCall> tool_calls;

        printf("[llm] Sending request to OpenRouter...\n");
        if (!llm.chat(wav.data(), wav.size(), response, tool_calls)) {
            fprintf(stderr, "[error] LLM request failed\n");
            break;
        }

        printf("[llm] Response received\n");

        if (tool_calls.empty()) {
            if (!response.empty()) {
                printf("[reply] %s\n", response.c_str());

                printf("[tts] Requesting speech synthesis for: \"%s\"\n", response.c_str());
                printf("[tts] Starting streaming playback...\n");
                if (tts.text_to_speech_stream(response.c_str(), player)) {
                    printf("[tts] Streaming playback complete\n");
                } else {
                    fprintf(stderr, "[error] TTS streaming failed\n");
                }
            } else {
                printf("[llm] Empty response from LLM\n");
            }
            break;
        }

        printf("[tools] Executing %zu tool call(s)...\n", tool_calls.size());
        for (size_t i = 0; i < tool_calls.size(); i++) {
            const aiden::ToolCall& tc = tool_calls[i];
            printf("  [tool] %s(%s)\n", tc.name.c_str(), tc.arguments.c_str());
            std::string result = execute_tool(config.hid_binary,
                                             tc.name.c_str(),
                                             tc.arguments.c_str());
            printf("  [result] %s\n", result.c_str());
            llm.add_tool_result(tc.id.c_str(), result.c_str());
        }

        wav.clear();
    }
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);

    bool manual_mode = false;
    const char* config_path = "agent.conf";

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--manual") == 0) {
            manual_mode = true;
        } else {
            config_path = argv[i];
        }
    }

    aiden::AgentConfig config;
    if (!aiden::load_config(config_path, config)) {
        fprintf(stderr, "[error] Failed to load config from %s\n", config_path);
        fprintf(stderr, "Usage: %s [--manual] [config_file]\n", argv[0]);
        fprintf(stderr, "  --manual: Use manual trigger (press Enter) instead of GPIO wakeup\n");
        return 1;
    }

    printf("[init] Config loaded: model=%s, threshold=%d, silence=%dms\n",
           config.llm_model, config.energy_threshold, config.silence_ms);

    aiden::HttpClient http;
    if (!http.is_available()) {
        fprintf(stderr, "[error] curl not found. Install curl to use this agent.\n");
        return 1;
    }

    aiden::WakeupListener wakeup;
    if (!manual_mode) {
        printf("[init] Starting wakeup listener on GPIO 33...\n");
        if (!wakeup.start(33, on_wakeup)) {
            fprintf(stderr, "[error] Failed to start wakeup listener\n");
            return 1;
        }
    } else {
        printf("[init] Manual trigger mode enabled\n");
    }

    printf("[init] Initializing audio (16kHz/16bit/mono)...\n");
    aiden::AudioConfig audio_cfg;
    audio_cfg.sample_rate = 16000;
    audio_cfg.channels = 1;
    audio_cfg.bit_width = 16;

    aiden::AudioCapture capture;
    if (!capture.init(audio_cfg)) {
        fprintf(stderr, "[error] Failed to initialize audio capture\n");
        return 1;
    }

    aiden::AudioPlayer player;
    if (!player.init(audio_cfg)) {
        fprintf(stderr, "[error] Failed to initialize audio player\n");
        return 1;
    }

    aiden::OpenRouterClient llm(config.api_key, config.llm_model, config.tts_model,
                                config.additional_prompt);
    aiden::MinimaxTTS tts(config.minimax_api_key, config.minimax_voice_id,
                          config.minimax_emotion, config.minimax_speed);
    aiden::AudioVAD vad(16000, config.energy_threshold, config.silence_ms,
                        config.min_speech_ms, manual_mode);

    if (manual_mode)
        printf("\n[ready] Press Enter to start recording, press Enter again to stop\n");
    else
        printf("\n[ready] Waiting for wakeup event (GPIO 33)... Ctrl+C to quit\n\n");

    while (!quit) {
        if (!wakeup_triggered) {
            if (manual_mode) {
                printf("\n[manual] Press Enter to start...\n");
                int ch = getchar();
                if (ch == EOF) break;
                drain_stdin();
                printf("[manual] Recording started, press Enter to stop...\n");
                wakeup_triggered = true;
            } else {
                usleep(100000);
                continue;
            }
        }

        printf("[listen] Recording audio...\n");
        vad.reset();

        bool manual_stop = false;
        while (wakeup_triggered && !quit) {
            // Check for manual stop in manual mode
            if (manual_mode && stdin_has_data()) {
                getchar();
                drain_stdin();
                printf("[manual] Stop triggered by user\n");
                manual_stop = true;
                break;
            }

            aiden::AudioFrame frame;
            if (!capture.get_frame(frame)) {
                usleep(10000);
                continue;
            }

            const std::vector<int16_t>* utterance = vad.process(
                (int16_t*)frame.data, frame.length / 2);
            capture.release_frame();

            if (utterance) {
                printf("[utterance] VAD detected end of speech\n");
                process_utterance(*utterance, llm, tts, player, config);
                wakeup_triggered = false;
                printf("\n[ready] Waiting for next wakeup event...\n\n");
                break;
            }
        }

        if (manual_stop) {
            const std::vector<int16_t>* utterance = vad.flush();
            if (utterance && !utterance->empty()) {
                printf("[manual] Sending buffered audio without waiting for VAD\n");
                process_utterance(*utterance, llm, tts, player, config);
            } else {
                printf("[manual] No buffered audio to send\n");
            }
            wakeup_triggered = false;
            printf("\n[ready] Waiting for next wakeup event...\n\n");
        }
    }

    wakeup.stop();
    capture.stop();
    printf("\n[exit] Stopped.\n");
    return 0;
}
