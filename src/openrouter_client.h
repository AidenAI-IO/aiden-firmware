#pragma once
#include <string>
#include <vector>
#include <cstdint>

namespace aiden {

struct ToolCall {
    std::string id;
    std::string name;
    std::string arguments;
};

class OpenRouterClient {
public:
    OpenRouterClient(const char* api_key, const char* llm_model,
                     const char* additional_prompt = "");

    bool chat(const uint8_t* wav_data, size_t wav_len,
              std::string& response, std::vector<ToolCall>& tool_calls);

    void add_tool_result(const char* tool_call_id, const char* result);

private:
    std::string api_key_;
    std::string llm_model_;
    std::string conversation_;
};

}
