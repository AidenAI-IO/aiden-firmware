#include "aiden_sdk.h"
#include "agent_version.h"
#include "audio_service_client.h"
#include "config.h"
#include "http_client.h"
#include "provider_factory.h"
#include "text_input.h"
#include "tool_dispatch.h"
#include "tool_image_attachment.h"
#include "vad.h"
#include "wav_codec.h"
#include <memory>
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

static volatile sig_atomic_t quit = 0;
static volatile bool wakeup_triggered = false;

static void signal_handler(int) { quit = 1; }

static void install_signal_handler() {
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = signal_handler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
}

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

class TextInputTerminalMode {
public:
    explicit TextInputTerminalMode(int fd) : fd_(fd), enabled_(false) {
        if (!isatty(fd_)) return;
        if (tcgetattr(fd_, &original_) != 0) return;

        struct termios tio = original_;
#ifdef IUTF8
        tio.c_iflag |= IUTF8;
#endif
        // Keep Ctrl-C on SIGINT; the 0x03 byte handler below is defensive only.
        tio.c_lflag |= ISIG;
        tio.c_lflag &= ~(ICANON | ECHO);
        tio.c_cc[VINTR] = 0x03;
        tio.c_cc[VMIN] = 1;
        tio.c_cc[VTIME] = 0;

        enabled_ = tcsetattr(fd_, TCSANOW, &tio) == 0;
    }

    ~TextInputTerminalMode() {
        if (enabled_) tcsetattr(fd_, TCSANOW, &original_);
    }

    bool enabled() const { return enabled_; }

private:
    int fd_;
    bool enabled_;
    struct termios original_;
};

static void erase_display_columns(int columns) {
    for (int i = 0; i < columns; i++) putchar('\b');
    for (int i = 0; i < columns; i++) putchar(' ');
    for (int i = 0; i < columns; i++) putchar('\b');
    fflush(stdout);
}

static bool read_interactive_text_line(const char* prompt, std::string& line) {
    line.clear();
    printf("%s", prompt);
    fflush(stdout);

    aiden::TextInputState input_state;
    while (!quit) {
        unsigned char byte;
        ssize_t n = read(STDIN_FILENO, &byte, 1);
        if (n == 0) return false;
        if (n < 0) {
            if (errno == EINTR) return false;
            return false;
        }

        const std::string previous_line = line;
        const size_t previous_size = line.size();
        aiden::TextInputLineStatus status = aiden::apply_text_input_byte(input_state, line, byte);
        if (status == aiden::TextInputLineStatus::Complete) {
            printf("\n");
            fflush(stdout);
            return true;
        }
        if (status == aiden::TextInputLineStatus::Eof) return false;
        if (status == aiden::TextInputLineStatus::Interrupt) {
            quit = 1;
            return false;
        }

        if (line.size() > previous_size) {
            ssize_t written = write(STDOUT_FILENO, &byte, 1);
            (void)written;
        } else if (line.size() < previous_size) {
            erase_display_columns(aiden::text_display_width(previous_line.substr(line.size())));
        }
    }

    return false;
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

static std::string redact_proxy_for_log(const char* proxy) {
    if (!proxy) return "";

    std::string text(proxy);
    size_t authority_start = 0;
    size_t scheme_pos = text.find("://");
    if (scheme_pos != std::string::npos) authority_start = scheme_pos + 3;

    size_t at_pos = text.find('@', authority_start);
    if (at_pos == std::string::npos) return text;

    return text.substr(0, authority_start) + "***@" + text.substr(at_pos + 1);
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

static bool should_dump_last_utterance_wav() {
    const char* enabled = getenv("AIDEN_DEBUG_DUMP_LAST_UTTERANCE");
    return enabled && strcmp(enabled, "1") == 0;
}

static void append_pcm16le_samples(const std::vector<uint8_t>& pcm_bytes,
                                   std::vector<int16_t>& out_samples,
                                   bool* has_pending_byte,
                                   uint8_t* pending_byte) {
    if (!has_pending_byte || !pending_byte) return;

    size_t i = 0;
    if (*has_pending_byte && !pcm_bytes.empty()) {
        uint16_t word = static_cast<uint16_t>(*pending_byte) |
                        (static_cast<uint16_t>(pcm_bytes[0]) << 8);
        out_samples.push_back(static_cast<int16_t>(word));
        *has_pending_byte = false;
        i = 1;
    }

    for (; i + 1 < pcm_bytes.size(); i += 2) {
        uint16_t word = static_cast<uint16_t>(pcm_bytes[i]) |
                        (static_cast<uint16_t>(pcm_bytes[i + 1]) << 8);
        out_samples.push_back(static_cast<int16_t>(word));
    }

    if (i < pcm_bytes.size()) {
        *pending_byte = pcm_bytes[i];
        *has_pending_byte = true;
    }
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

    const size_t chunk_samples = 16000 * 60;   // ~60s (tencent_asr max)
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

static void run_after_first_turn(aiden::LlmClient& llm,
                                 aiden::TtsClient& tts,
                                 aiden::AudioServiceClient& audio,
                                 const aiden::AgentConfig& config,
                                 std::string response,
                                 std::vector<aiden::ToolCall> tool_calls) {
    while (true) {
        if (tool_calls.empty()) {
            if (!response.empty()) {
                printf("[reply] %s\n", response.c_str());

                printf("[tts] Requesting speech synthesis for: \"%s\"\n", response.c_str());
                printf("[tts] Starting streaming playback...\n");
                if (tts.text_to_speech_stream(response.c_str(), audio)) {
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
                              aiden::AudioServiceClient& audio,
                              const aiden::AgentConfig& config,
                              aiden::SttClient* stt) {
    float duration = utterance.size() / 16000.0f;
    printf("[utterance] %.1fs of speech\n", duration);

    std::vector<uint8_t> wav = aiden::pcm16_mono_16khz_to_wav(utterance);
    printf("[debug] WAV size: %zu bytes\n", wav.size());

    // Keep raw utterance dumps opt-in to avoid persisting user audio by default.
    if (should_dump_last_utterance_wav()) {
        FILE* f = fopen("/tmp/last_utterance.wav", "wb");
        if (f) { fwrite(wav.data(), 1, wav.size(), f); fclose(f); }
    }

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
    run_after_first_turn(llm, tts, audio, config, response, tool_calls);
}

static void process_text_input(const std::string& text,
                               aiden::LlmClient& llm,
                               aiden::TtsClient& tts,
                               aiden::AudioServiceClient& audio,
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

    run_after_first_turn(llm, tts, audio, config, response, tool_calls);
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

    install_signal_handler();

    TriggerMode mode = MODE_WAKEUP;
    const char* config_path = NULL;

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

    if (!config_path) {
        if (access("agent.conf", F_OK) == 0) {
            config_path = "agent.conf";
        } else {
            config_path = "/userdata/agent.conf";
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

    if (config.network.proxy[0] != '\0') {
        setenv("http_proxy", config.network.proxy, 1);
        setenv("https_proxy", config.network.proxy, 1);
        setenv("HTTP_PROXY", config.network.proxy, 1);
        setenv("HTTPS_PROXY", config.network.proxy, 1);
        std::string redacted_proxy = redact_proxy_for_log(config.network.proxy);
        printf("[init] Proxy set: %s\n", redacted_proxy.c_str());
    }

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

    // Connect to audio_service.
    const char* audio_sock = config.audio_service_socket[0] != '\0'
                             ? config.audio_service_socket
                             : "/run/audio_service/audio_service.sock";
    aiden::AudioServiceClient audio_client(audio_sock);

    // Record session state (opened per utterance, closed after VAD end).
    uint64_t record_session_id = 0;
    bool record_active = false;

    aiden::AudioFormat audio_fmt;
    audio_fmt.sample_rate = 16000;
    audio_fmt.channels    = 1;
    audio_fmt.bit_width   = 16;

    auto start_capture = [&]() -> bool {
        if (mode == MODE_TEXT || record_active) return true;
        printf("[audio] Opening record session on audio_service...\n");
        aiden::RecordStartResult rs;
        if (audio_client.start_recording(audio_fmt, &rs) != aiden::AidenServiceStatus::OK) {
            fprintf(stderr, "[error] Failed to start recording session\n");
            return false;
        }
        record_session_id = rs.session_id;
        record_active = true;
        return true;
    };
    auto stop_capture = [&]() {
        if (mode == MODE_TEXT || !record_active) return;
        audio_client.stop_recording(record_session_id);
        record_active = false;
        record_session_id = 0;
        printf("[audio] Record session closed\n");
    };

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
        TextInputTerminalMode terminal_mode(STDIN_FILENO);

        printf("\n[ready] Type a command and press Enter (Ctrl+D or Ctrl+C to quit)\n");
        while (!quit) {
            std::string line;
            if (terminal_mode.enabled()) {
                printf("\n");
                if (!read_interactive_text_line("> ", line)) break;
            } else {
                printf("\n> ");
                fflush(stdout);
                if (!std::getline(std::cin, line)) break;
            }

            if (line.empty()) continue;

            process_text_input(line, *llm, *tts, audio_client, runtime_config);
        }
    } else if (mode == MODE_MANUAL) {
        printf("\n[ready] Press Enter to start recording, press Enter again to stop\n");
    } else {
        printf("\n[ready] Waiting for wakeup event (GPIO 33)... Ctrl+C to quit\n\n");
    }

    std::vector<int16_t> vad_pending;

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
        vad_pending.clear();
        bool has_pending_pcm_byte = false;
        uint8_t pending_pcm_byte = 0;

        bool manual_stop = false;
        while (wakeup_triggered && !quit) {
            // Check for manual stop in manual mode.
            if (mode == MODE_MANUAL && stdin_has_data()) {
                getchar();
                drain_stdin();
                printf("[manual] Stop triggered by user\n");
                manual_stop = true;
                break;
            }

            aiden::AudioChunkResult chunk;
            aiden::AidenServiceStatus cs =
                audio_client.read_record_chunk(record_session_id, 200, &chunk);
            if (cs == aiden::AidenServiceStatus::TIMEOUT) continue;
            if (cs != aiden::AidenServiceStatus::OK) {
                fprintf(stderr, "[listen] read_record_chunk error, stopping\n");
                // Keep session state until stop_capture() runs, so we can do a
                // best-effort close of the server-side recording session.
                wakeup_triggered = false;
                break;
            }
            if (chunk.end_of_stream) {
                fprintf(stderr, "[listen] record session closed by service\n");
                record_active = false;
                record_session_id = 0;
                wakeup_triggered = false;
                break;
            }
            if (chunk.pcm.empty()) continue;

            const std::vector<int16_t>* utterance = nullptr;
            {
                append_pcm16le_samples(chunk.pcm, vad_pending,
                                       &has_pending_pcm_byte, &pending_pcm_byte);

                // Feed VAD in 30ms frames (480 samples at 16kHz). Keep remainder
                // between chunks so no tail samples are dropped.
                const int kFrameSamples = 480;
                size_t consumed = 0;
                while (consumed + kFrameSamples <= vad_pending.size()) {
                    utterance = vad.process(vad_pending.data() + consumed, kFrameSamples);
                    consumed += kFrameSamples;
                    if (utterance) break;
                }
                if (consumed > 0) {
                    vad_pending.erase(vad_pending.begin(), vad_pending.begin() + consumed);
                }
            }

            if (utterance) {
                printf("[utterance] VAD detected end of speech\n");
                process_utterance(*utterance, *llm, *tts, audio_client, runtime_config, stt.get());
                wakeup_triggered = false;
                printf("\n[ready] Waiting for next wakeup event...\n\n");
                break;
            }
        }

        if (manual_stop) {
            if (!vad_pending.empty()) {
                // Preserve any tail samples not aligned to 30ms frame.
                vad.process(vad_pending.data(), static_cast<int>(vad_pending.size()));
                vad_pending.clear();
            }
            const std::vector<int16_t>* utterance = vad.flush();
            printf("[debug] vad.flush() returned %zu samples\n",
                   utterance ? utterance->size() : 0);
            if (utterance && !utterance->empty()) {
                printf("[manual] Sending buffered audio without waiting for VAD\n");
                process_utterance(*utterance, *llm, *tts, audio_client, runtime_config, stt.get());
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
