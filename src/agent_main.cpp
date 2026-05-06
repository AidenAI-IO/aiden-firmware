#include "aiden_sdk.h"
#include "config.h"
#include "http_client.h"
#include "provider_factory.h"
#include "tool_dispatch.h"
#include "vad.h"
#include "wav_codec.h"
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

static std::string execute_tool(const char* hid_binary, const char* tool_name,
                                const char* args_json) {
    aiden::ToolCommandResult command = aiden::build_tool_command(hid_binary, tool_name, args_json);
    if (!command.ok)
        return command.error;

    FILE* fp = popen(command.command.c_str(), "r");
    if (!fp) return "error: popen failed";

    char buf[256];
    std::string result;
    while (fgets(buf, sizeof(buf), fp))
        result += buf;

    pclose(fp);
    return result.empty() ? "ok" : result;
}

static void process_utterance(const std::vector<int16_t>& utterance,
                              aiden::LlmClient& llm,
                              aiden::TtsClient& tts,
                              aiden::AudioPlayer& player,
                              const aiden::AgentConfig& config) {
    float duration = utterance.size() / 16000.0f;
    printf("[utterance] %.1fs of speech\n", duration);

    std::vector<uint8_t> wav = aiden::pcm16_mono_16khz_to_wav(utterance);
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
           config.model.model, config.energy_threshold, config.silence_ms);

    aiden::ProviderCheckResult provider_check = aiden::check_provider_config(config);
    if (!provider_check.ok) {
        fprintf(stderr, "[error] %s\n", provider_check.error.c_str());
        return 1;
    }

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

    std::string provider_error;
    std::unique_ptr<aiden::LlmClient> llm = aiden::create_llm_client(config, provider_error);
    if (!llm) {
        fprintf(stderr, "[error] %s\n", provider_error.c_str());
        return 1;
    }

    provider_error.clear();
    std::unique_ptr<aiden::TtsClient> tts = aiden::create_tts_client(config, provider_error);
    if (!tts) {
        fprintf(stderr, "[error] %s\n", provider_error.c_str());
        return 1;
    }

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
                process_utterance(*utterance, *llm, *tts, player, config);
                wakeup_triggered = false;
                printf("\n[ready] Waiting for next wakeup event...\n\n");
                break;
            }
        }

        if (manual_stop) {
            const std::vector<int16_t>* utterance = vad.flush();
            if (utterance && !utterance->empty()) {
                printf("[manual] Sending buffered audio without waiting for VAD\n");
                process_utterance(*utterance, *llm, *tts, player, config);
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
