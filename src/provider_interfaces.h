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
    virtual void add_tool_result(const char* tool_call_id, const char* result) = 0;
};

class TtsClient {
public:
    virtual ~TtsClient() {}
    virtual bool text_to_speech_stream(const char* text, class AudioPlayer& player) = 0;
};

}
