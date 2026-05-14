#pragma once
#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace aiden {

struct ToolCall {
    std::string id;
    std::string name;
    std::string arguments;
};

class LlmClient {
public:
    virtual ~LlmClient() {}
    virtual bool chat(const uint8_t* wav_data, size_t wav_len,
                      std::string& response,
                      std::vector<ToolCall>& tool_calls) = 0;
    virtual bool chat_text(const char* text,
                           std::string& response,
                           std::vector<ToolCall>& tool_calls) = 0;
    virtual void add_tool_result(const char* tool_call_id, const char* result) = 0;
    virtual void add_user_image_url(const char* data_url, const char* text) = 0;
};

class TtsClient {
public:
    virtual ~TtsClient() {}
    // Stream TTS audio into an AudioServiceClient playback session.
    virtual bool text_to_speech_stream(const char* text,
                                       class AudioServiceClient& audio) = 0;
};

class SttClient {
public:
    virtual ~SttClient() {}
    virtual bool transcribe_wav(const uint8_t* wav_data, size_t wav_len, std::string& text) = 0;
};

}
