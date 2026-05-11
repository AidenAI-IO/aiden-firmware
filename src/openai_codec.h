#pragma once
#include "provider_interfaces.h"
#include <cstddef>
#include <cstdint>
#include <string>

namespace aiden {
namespace openai {

struct ChatResult {
    std::string content;
    std::vector<ToolCall> tool_calls;
    std::string assistant_message_json;
    bool ok;
    std::string error;

    ChatResult() : ok(false) {}
};

std::string base64_encode(const uint8_t* data, size_t len);
std::string build_tool_definitions_json();
ChatResult parse_chat_response(const std::string& http_response);

std::string init_conversation(const std::string& system_prompt);
std::string append_user_audio_wav(const std::string& conversation_json,
                                  const std::string& base64_wav);
std::string append_user_audio_wav_if_present(const std::string& conversation_json,
                                             const uint8_t* wav_data,
                                             size_t wav_len);
std::string append_user_text(const std::string& conversation_json,
                             const char* text);
std::string append_user_image_url(const std::string& conversation_json,
                                  const char* data_url,
                                  const char* text);
std::string append_assistant_message(const std::string& conversation_json,
                                      const std::string& assistant_message_json);
std::string append_tool_result(const std::string& conversation_json,
                               const std::string& tool_call_id,
                               const std::string& result);
std::string build_chat_request(const std::string& conversation_json,
                               const std::string& model);

}
}
