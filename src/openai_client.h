#pragma once
#include "provider_interfaces.h"
#include <string>
#include <vector>
#include <cstdint>

namespace aiden {

class OpenAIClient : public LlmClient {
public:
    OpenAIClient(const char* api_key, const char* llm_model,
                 const char* base_url = "",
                 const char* additional_prompt = "");

    bool chat(const uint8_t* wav_data, size_t wav_len,
              std::string& response, std::vector<ToolCall>& tool_calls) override;

    bool chat_text(const char* text,
                   std::string& response, std::vector<ToolCall>& tool_calls) override;

    void add_tool_result(const char* tool_call_id, const char* result) override;
    void add_user_image_url(const char* data_url, const char* text) override;

private:
    std::string api_key_;
    std::string llm_model_;
    std::string base_url_;
    std::string conversation_;
};

}
