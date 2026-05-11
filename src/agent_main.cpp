#include "aiden_sdk.h"
#include "agent_version.h"
#include "config.h"
#include "http_client.h"
#include "provider_factory.h"
#include "tool_dispatch.h"
#include "tool_image_attachment.h"
#include "vad.h"
#include "wav_codec.h"
#include <memory>
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <stdlib.h>
#include <signal.h>
#include <unistd.h>
#include <errno.h>
#include <string.h>
#include <sys/select.h>
#include <sys/wait.h>
#include <termios.h>
#include <iostream>
#include <vector>
#include <string>
#include <cstdlib>

enum TriggerMode {
    MODE_WAKEUP,
    MODE_MANUAL,
    MODE_TEXT
};

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

static const char* frame_socket_path(const aiden::AgentConfig& config) {
    const char* env_socket = getenv("FRAME_SERVICE_SOCKET");
    if (env_socket && env_socket[0] != '\0') return env_socket;
    if (config.frame_service_socket[0] != '\0') return config.frame_service_socket;
    return "/tmp/frame_service.sock";
}

static std::string execute_tool(const aiden::AgentConfig& config,
                                const char* tool_name,
                                const char* args_json) {
    if (aiden::is_frame_tool(tool_name)) {
        return aiden::handle_frame_tool(frame_socket_path(config), tool_name, args_json, "/tmp");
    }

    aiden::ToolCommandResult command = aiden::build_tool_command(config.hid_binary, tool_name, args_json);
    if (!command.ok)
        return command.error;

    FILE* fp = popen(command.command.c_str(), "r");
    if (!fp) return "error: popen failed";

    char buf[256];
    std::string result;
    while (fgets(buf, sizeof(buf), fp))
        result += buf;

    int status = pclose(fp);
    if (status == -1) {
        char err[128];
        snprintf(err, sizeof(err), "error: tool command failed: %s", strerror(errno));
        return err;
    }
    if (WIFEXITED(status) && WEXITSTATUS(status) != 0) {
        char err[128];
        snprintf(err, sizeof(err), "error: tool command exited with status %d",
                 WEXITSTATUS(status));
        return result.empty() ? std::string(err) : std::string(err) + ": " + result;
    }
    if (WIFSIGNALED(status)) {
        char err[128];
        snprintf(err, sizeof(err), "error: tool command terminated by signal %d",
                 WTERMSIG(status));
        return result.empty() ? std::string(err) : std::string(err) + ": " + result;
    }
    if (!WIFEXITED(status)) {
        char err[128];
        snprintf(err, sizeof(err), "error: tool command ended abnormally: status %d", status);
        return result.empty() ? std::string(err) : std::string(err) + ": " + result;
    }
    return result.empty() ? "ok" : result;
}

static bool is_stt_then_text_mode(const aiden::AgentConfig& config) {
    return strcmp(config.asr_mode, "stt_then_text") == 0;
}

static bool has_any_model_text_override(const aiden::AgentConfig& config) {
    return config.model_text.provider[0] != '\0' ||
           config.model_text.api_key[0] != '\0' ||
           config.model_text.model[0] != '\0' ||
           config.model_text.base_url[0] != '\0';
}

static void copy_if_non_empty(char* dst, size_t dst_size, const char* src) {
    if (!src || src[0] == '\0') return;
    strncpy(dst, src, dst_size - 1);
    dst[dst_size - 1] = '\0';
}

static aiden::AgentConfig make_runtime_config(const aiden::AgentConfig& config) {
    aiden::AgentConfig runtime = config;
    if (is_stt_then_text_mode(config) && has_any_model_text_override(config)) {
        copy_if_non_empty(runtime.model.provider, sizeof(runtime.model.provider), config.model_text.provider);
        copy_if_non_empty(runtime.model.api_key, sizeof(runtime.model.api_key), config.model_text.api_key);
        copy_if_non_empty(runtime.model.model, sizeof(runtime.model.model), config.model_text.model);
        copy_if_non_empty(runtime.model.base_url, sizeof(runtime.model.base_url), config.model_text.base_url);
    }
    return runtime;
}

static void append_transcript_piece(std::string& merged, const std::string& piece) {
    if (piece.empty()) return;
    if (!merged.empty()) merged += " ";
    merged += piece;
}

static bool transcribe_with_auto_chunk(aiden::SttClient* stt,
                                       const std::vector<int16_t>& pcm16_16k_mono,
                                       std::string& transcript) {
    transcript.clear();
    if (!stt || pcm16_16k_mono.empty()) return false;

    const size_t chunk_samples = 16000 * 6;    // ~6s
    const size_t overlap_samples = 16000 / 3;  // ~333ms

    if (pcm16_16k_mono.size() <= chunk_samples) {
        std::vector<uint8_t> wav = aiden::pcm16_mono_16khz_to_wav(pcm16_16k_mono);
        return stt->transcribe_wav(wav.data(), wav.size(), transcript);
    }

    size_t start = 0;
    int chunk_idx = 0;
    while (start < pcm16_16k_mono.size()) {
        size_t end = start + chunk_samples;
        if (end > pcm16_16k_mono.size()) end = pcm16_16k_mono.size();
        std::vector<int16_t> chunk(pcm16_16k_mono.begin() + start, pcm16_16k_mono.begin() + end);
        std::vector<uint8_t> wav = aiden::pcm16_mono_16khz_to_wav(chunk);

        std::string piece;
        if (!stt->transcribe_wav(wav.data(), wav.size(), piece)) {
            fprintf(stderr, "[error] STT chunk %d transcription failed\n", chunk_idx);
            return false;
        }
        printf("[stt] Chunk %d transcript: %s\n", chunk_idx, piece.c_str());
        append_transcript_piece(transcript, piece);

        if (end >= pcm16_16k_mono.size()) break;
        start = end - overlap_samples;
        chunk_idx++;
    }
    return !transcript.empty();
}

static void downsample_mono_to_16k(const int16_t* in,
                                   int in_samples,
                                   int input_rate,
                                   std::vector<int16_t>& out) {
    out.clear();
    if (!in || in_samples <= 0) return;
    if (input_rate <= 16000) {
        out.assign(in, in + in_samples);
        return;
    }

    if (input_rate == 32000) {
        int n = in_samples / 2;
        out.resize((size_t)n);
        for (int i = 0; i < n; ++i) {
            int a = in[i * 2];
            int b = in[i * 2 + 1];
            out[(size_t)i] = (int16_t)((a + b) / 2);
        }
        return;
    }

    // Generic fallback: nearest-neighbor resampling to 16k.
    size_t out_count = (size_t)((int64_t)in_samples * 16000 / input_rate);
    if (out_count == 0) out_count = 1;
    out.resize(out_count);
    for (size_t i = 0; i < out_count; ++i) {
        size_t src = (size_t)((int64_t)i * input_rate / 16000);
        if (src >= (size_t)in_samples) src = (size_t)in_samples - 1;
        out[i] = in[src];
    }
}

static void run_after_first_turn(aiden::LlmClient& llm,
                                 aiden::TtsClient& tts,
                                 aiden::AudioPlayer& player,
                                 const aiden::AgentConfig& config,
                                 std::string response,
                                 std::vector<aiden::ToolCall> tool_calls) {
    while (true) {
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
            return;
        }

        printf("[tools] Executing %zu tool call(s)...\n", tool_calls.size());
        std::vector<aiden::ImageAttachment> image_attachments;
        for (size_t i = 0; i < tool_calls.size(); i++) {
            const aiden::ToolCall& tc = tool_calls[i];
            printf("  [tool] %s(%s)\n", tc.name.c_str(), tc.arguments.c_str());
            std::string result = execute_tool(config,
                                              tc.name.c_str(),
                                              tc.arguments.c_str());
            printf("  [result] %s\n", result.c_str());
            llm.add_tool_result(tc.id.c_str(), result.c_str());
            aiden::ImageAttachment attachment;
            if (aiden::build_image_attachment_from_tool_result(tc.name.c_str(), result.c_str(), &attachment)) {
                image_attachments.push_back(attachment);
            }
        }
        for (size_t i = 0; i < image_attachments.size(); ++i) {
            llm.add_user_image_url(image_attachments[i].data_url.c_str(),
                                   image_attachments[i].text.c_str());
        }

        response.clear();
        tool_calls.clear();

        printf("[llm] Sending tool results to provider '%s' (model=%s)...\n",
               config.model.provider, config.model.model);
        if (!llm.chat(NULL, 0, response, tool_calls)) {
            fprintf(stderr, "[error] LLM request failed\n");
            return;
        }
        printf("[llm] Response received\n");
    }
}

static void process_utterance(const std::vector<int16_t>& utterance,
                              aiden::LlmClient& llm,
                              aiden::TtsClient& tts,
                              aiden::AudioPlayer& player,
                              const aiden::AgentConfig& config,
                              aiden::SttClient* stt) {
    float duration = utterance.size() / 16000.0f;
    printf("[utterance] %.1fs of speech\n", duration);

    std::vector<uint8_t> wav = aiden::pcm16_mono_16khz_to_wav(utterance);
    printf("[debug] WAV size: %zu bytes\n", wav.size());

    std::string response;
    std::vector<aiden::ToolCall> tool_calls;

    printf("[llm] Sending request to provider '%s' (model=%s)...\n",
           config.model.provider, config.model.model);

    if (is_stt_then_text_mode(config)) {
        if (!stt) {
            fprintf(stderr, "[error] STT mode enabled but STT client is unavailable\n");
            return;
        }

        std::string transcript;
        printf("[stt] Transcribing audio with provider '%s' (engine=%s)...\n",
               config.stt.provider, config.stt.engine_model_type);
        if (!transcribe_with_auto_chunk(stt, utterance, transcript)) {
            fprintf(stderr, "[error] STT transcription failed\n");
            return;
        }
        if (transcript.empty()) {
            fprintf(stderr, "[error] STT returned empty transcript\n");
            return;
        }

        printf("[stt] Transcript: %s\n", transcript.c_str());
        if (!llm.chat_text(transcript.c_str(), response, tool_calls)) {
            fprintf(stderr, "[error] LLM request failed\n");
            return;
        }
    } else {
        if (!llm.chat(wav.data(), wav.size(), response, tool_calls)) {
            fprintf(stderr, "[error] LLM request failed\n");
            return;
        }
    }

    printf("[llm] Response received\n");
    run_after_first_turn(llm, tts, player, config, response, tool_calls);
}

static void process_text_input(const std::string& text,
                               aiden::LlmClient& llm,
                               aiden::TtsClient& tts,
                               aiden::AudioPlayer& player,
                               const aiden::AgentConfig& config) {
    printf("[text] %s\n", text.c_str());

    std::string response;
    std::vector<aiden::ToolCall> tool_calls;

    printf("[llm] Sending request to provider '%s' (model=%s)...\n",
           config.model.provider, config.model.model);
    if (!llm.chat_text(text.c_str(), response, tool_calls)) {
        fprintf(stderr, "[error] LLM request failed\n");
        return;
    }
    printf("[llm] Response received\n");

    run_after_first_turn(llm, tts, player, config, response, tool_calls);
}

static bool parse_mode_value(const char* v, TriggerMode& mode) {
    if (strcmp(v, "wakeup") == 0) { mode = MODE_WAKEUP; return true; }
    if (strcmp(v, "manual") == 0) { mode = MODE_MANUAL; return true; }
    if (strcmp(v, "text") == 0)   { mode = MODE_TEXT;   return true; }
    return false;
}

int main(int argc, char* argv[]) {
    if (aiden::is_agent_version_command(argc, argv)) {
        printf("version: %s\n", aiden::agent_version());
        printf("commit_time: %s\n", aiden::agent_commit_time());
        return 0;
    }

    signal(SIGINT, signal_handler);

    TriggerMode mode = MODE_WAKEUP;
    const char* config_path = "agent.conf";

    for (int i = 1; i < argc; i++) {
        if (strncmp(argv[i], "--mode=", 7) == 0) {
            const char* v = argv[i] + 7;
            if (!parse_mode_value(v, mode)) {
                fprintf(stderr, "[error] Unknown mode: %s (expected wakeup|manual|text)\n", v);
                return 1;
            }
        } else if (strcmp(argv[i], "--mode") == 0) {
            if (i + 1 >= argc) {
                fprintf(stderr, "[error] --mode requires a value (wakeup|manual|text)\n");
                return 1;
            }
            const char* v = argv[++i];
            if (!parse_mode_value(v, mode)) {
                fprintf(stderr, "[error] Unknown mode: %s (expected wakeup|manual|text)\n", v);
                return 1;
            }
        } else {
            config_path = argv[i];
        }
    }

    aiden::AgentConfig config;
    std::string config_error;
    if (!aiden::load_config(config_path, config, &config_error)) {
        fprintf(stderr, "[error] Failed to load config from %s: %s\n",
                config_path, config_error.c_str());
        fprintf(stderr, "Usage: %s [--mode=wakeup|manual|text] [config_file]\n", argv[0]);
        fprintf(stderr, "       %s version|--version|-v\n", argv[0]);
        fprintf(stderr, "  --mode=wakeup: Use GPIO 33 wakeup trigger (default)\n");
        fprintf(stderr, "  --mode=manual: Press Enter to start/stop audio recording\n");
        fprintf(stderr, "  --mode=text:   Type commands directly via stdin\n");
        return 1;
    }

    aiden::AgentConfig runtime_config = make_runtime_config(config);
    printf("[init] Config loaded: model=%s, asr_mode=%s, threshold=%d, silence=%dms\n",
           runtime_config.model.model, config.asr_mode, config.energy_threshold, config.silence_ms);

    if (strcmp(config.asr_mode, "direct_audio") != 0 && strcmp(config.asr_mode, "stt_then_text") != 0) {
        fprintf(stderr, "[error] Unknown asr_mode: %s (expected direct_audio|stt_then_text)\n", config.asr_mode);
        return 1;
    }

    aiden::ProviderCheckResult provider_check = aiden::check_provider_config(runtime_config);
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
    if (mode == MODE_WAKEUP) {
        printf("[init] Starting wakeup listener on GPIO 33...\n");
        if (!wakeup.start(33, on_wakeup)) {
            fprintf(stderr, "[error] Failed to start wakeup listener\n");
            return 1;
        }
    } else if (mode == MODE_MANUAL) {
        printf("[init] Manual trigger mode enabled\n");
    } else {
        printf("[init] Text input mode enabled\n");
    }

    aiden::AudioConfig audio_cfg;
    audio_cfg.sample_rate = 16000;
    audio_cfg.channels = 1;
    audio_cfg.bit_width = 16;

    aiden::AudioCapture capture;
    bool capture_active = false;
    auto start_capture = [&]() -> bool {
        if (mode == MODE_TEXT || capture_active) return true;
        printf("[audio] Opening capture device (16kHz/16bit/mono)...\n");
        if (!capture.init(audio_cfg)) {
            fprintf(stderr, "[error] Failed to initialize audio capture\n");
            return false;
        }
        capture_active = true;
        return true;
    };
    auto stop_capture = [&]() {
        if (mode == MODE_TEXT || !capture_active) return;
        capture.stop();
        capture_active = false;
        printf("[audio] Capture device closed\n");
    };

    aiden::AudioPlayer player;
    if (!player.init(audio_cfg)) {
        fprintf(stderr, "[error] Failed to initialize audio player\n");
        return 1;
    }

    std::string provider_error;
    std::unique_ptr<aiden::LlmClient> llm = aiden::create_llm_client(runtime_config, provider_error);
    if (!llm) {
        fprintf(stderr, "[error] %s\n", provider_error.c_str());
        return 1;
    }

    provider_error.clear();
    std::unique_ptr<aiden::TtsClient> tts = aiden::create_tts_client(runtime_config, provider_error);
    if (!tts) {
        fprintf(stderr, "[error] %s\n", provider_error.c_str());
        return 1;
    }

    provider_error.clear();
    std::unique_ptr<aiden::SttClient> stt;
    if (is_stt_then_text_mode(runtime_config)) {
        stt = aiden::create_stt_client(runtime_config, provider_error);
        if (!stt) {
            fprintf(stderr, "[error] %s\n", provider_error.c_str());
            return 1;
        }
    }

    aiden::AudioVAD vad(16000, config.energy_threshold, config.silence_ms,
                        config.min_speech_ms, mode == MODE_MANUAL);

    if (mode == MODE_TEXT) {
        // Enable IUTF8 so the TTY driver erases multi-byte UTF-8 chars (e.g. CJK)
        // as a single unit on backspace, instead of byte-by-byte.
        struct termios tio;
        if (tcgetattr(STDIN_FILENO, &tio) == 0) {
            tio.c_iflag |= IUTF8;
            tcsetattr(STDIN_FILENO, TCSANOW, &tio);
        }

        printf("\n[ready] Type a command and press Enter (Ctrl+D or Ctrl+C to quit)\n");
        while (!quit) {
            printf("\n> ");
            fflush(stdout);

            std::string line;
            if (!std::getline(std::cin, line)) break;

            if (line.empty()) continue;

            process_text_input(line, *llm, *tts, player, runtime_config);
        }
    } else if (mode == MODE_MANUAL) {
        printf("\n[ready] Press Enter to start recording, press Enter again to stop\n");
    } else {
        printf("\n[ready] Waiting for wakeup event (GPIO 33)... Ctrl+C to quit\n\n");
    }

    while (!quit && mode != MODE_TEXT) {
        if (!wakeup_triggered) {
            if (mode == MODE_MANUAL) {
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

        if (!start_capture()) {
            wakeup_triggered = false;
            continue;
        }

        printf("[listen] Recording audio...\n");
        vad.reset();
        int capture_channels = 0;
        std::vector<int16_t> mono_frame;
        std::vector<int16_t> resampled_frame;
        int detected_input_rate = 16000;
        uint64_t prev_frame_ts = 0;
        int rate_vote_count = 0;
        int rate_vote_32k = 0;

        bool manual_stop = false;
        while (wakeup_triggered && !quit) {
            // Check for manual stop in manual mode
            if (mode == MODE_MANUAL && stdin_has_data()) {
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

            const std::vector<int16_t>* utterance = nullptr;
            if (frame.data && frame.length >= 2) {
                int16_t* raw = reinterpret_cast<int16_t*>(frame.data);
                int sample_count = (int)(frame.length / sizeof(int16_t));

                if (capture_channels == 0) {
                    capture_channels = (frame.length >= 4096) ? 2 : 1;
                    printf("[audio] Detected capture channels: %d (frame_len=%u bytes)\n",
                           capture_channels, frame.length);
                }

                int samples_per_channel = sample_count / ((capture_channels == 2) ? 2 : 1);
                if (prev_frame_ts > 0 && frame.timestamp > prev_frame_ts && samples_per_channel > 0) {
                    uint64_t delta_us = frame.timestamp - prev_frame_ts;
                    if (delta_us > 0) {
                        double estimated = (double)samples_per_channel * 1000000.0 / (double)delta_us;
                        if (estimated > 24000.0 && estimated < 40000.0) rate_vote_32k++;
                        rate_vote_count++;
                        if (rate_vote_count >= 3) {
                            int new_rate = (rate_vote_32k >= 2) ? 32000 : 16000;
                            if (new_rate != detected_input_rate) {
                                detected_input_rate = new_rate;
                                printf("[audio] Estimated input sample rate: %d Hz\n", detected_input_rate);
                            }
                        }
                    }
                }
                prev_frame_ts = frame.timestamp;

                if (capture_channels == 2) {
                    int mono_count = sample_count / 2;
                    mono_frame.resize((size_t)mono_count);
                    for (int i = 0; i < mono_count; ++i) {
                        int left = raw[i * 2];
                        int right = raw[i * 2 + 1];
                        mono_frame[(size_t)i] = (int16_t)((left + right) / 2);
                    }
                    downsample_mono_to_16k(mono_frame.data(), mono_count,
                                           detected_input_rate, resampled_frame);
                    utterance = vad.process(resampled_frame.data(), (int)resampled_frame.size());
                } else {
                    downsample_mono_to_16k(raw, sample_count,
                                           detected_input_rate, resampled_frame);
                    utterance = vad.process(resampled_frame.data(), (int)resampled_frame.size());
                }
            }
            capture.release_frame();

            if (utterance) {
                printf("[utterance] VAD detected end of speech\n");
                process_utterance(*utterance, *llm, *tts, player, runtime_config, stt.get());
                wakeup_triggered = false;
                printf("\n[ready] Waiting for next wakeup event...\n\n");
                break;
            }
        }

        if (manual_stop) {
            const std::vector<int16_t>* utterance = vad.flush();
            if (utterance && !utterance->empty()) {
                printf("[manual] Sending buffered audio without waiting for VAD\n");
                process_utterance(*utterance, *llm, *tts, player, runtime_config, stt.get());
            } else {
                printf("[manual] No buffered audio to send\n");
            }
            wakeup_triggered = false;
            printf("\n[ready] Waiting for next wakeup event...\n\n");
        }

        stop_capture();
    }

    wakeup.stop();
    stop_capture();
    printf("\n[exit] Stopped.\n");
    return 0;
}
