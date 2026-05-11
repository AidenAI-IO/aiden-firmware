#include "openai_client.h"
#include "http_client.h"
#include "llm_api_url.h"
#include "openai_codec.h"
#include <stdio.h>
#include <stdlib.h>

namespace aiden {

OpenAIClient::OpenAIClient(const char* api_key, const char* llm_model,
                           const char* base_url,
                           const char* additional_prompt)
    : api_key_(api_key), llm_model_(llm_model), base_url_(base_url ? base_url : "") {
    std::string system_prompt =
        "You are an AI assistant controlling a device via keyboard and touchscreen. "
        "Use the provided tools to interact. Touch coordinates: 0-32767 absolute range.";

    if (additional_prompt && additional_prompt[0] != '\0') {
        system_prompt += "\n\n";
        system_prompt += additional_prompt;
    }

    conversation_ = openai::init_conversation(system_prompt);
}

bool OpenAIClient::chat(const uint8_t* wav_data, size_t wav_len,
                        std::string& response, std::vector<ToolCall>& tool_calls) {
    std::string conversation_with_user =
        openai::append_user_audio_wav_if_present(conversation_, wav_data, wav_len);
    std::string request_body = openai::build_chat_request(conversation_with_user, llm_model_);
    std::string url = build_chat_completions_url(base_url_.c_str(), "https://api.openai.com/v1");

    HttpClient http;
    std::string http_response;
    bool success = http.post_json(
        url.c_str(),
        api_key_.c_str(),
        request_body.c_str(),
        http_response);
    if (!success) {
        fprintf(stderr, "[error] HTTP request failed\n");
        fprintf(stderr, "[error] Response: %s\n", http_response.c_str());
        return false;
    }

    openai::ChatResult result = openai::parse_chat_response(http_response);
    if (!result.ok) {
        fprintf(stderr, "[error] Failed to parse OpenAI response: %s\n", result.error.c_str());
        fprintf(stderr, "[error] Raw response: %s\n", http_response.c_str());
        return false;
    }

    response = result.content;
    tool_calls = result.tool_calls;
    conversation_ = openai::append_assistant_message(conversation_with_user,
                                                     result.assistant_message_json);
    return true;
}

bool OpenAIClient::chat_text(const char* text,
                             std::string& response, std::vector<ToolCall>& tool_calls) {
    std::string conversation_with_user = openai::append_user_text(conversation_, text);
    std::string request_body = openai::build_chat_request(conversation_with_user, llm_model_);
    std::string url = build_chat_completions_url(base_url_.c_str(), "https://api.openai.com/v1");

    HttpClient http;
    std::string http_response;
    bool success = http.post_json(
        url.c_str(),
        api_key_.c_str(),
        request_body.c_str(),
        http_response);
    if (!success) {
        fprintf(stderr, "[error] HTTP request failed\n");
        fprintf(stderr, "[error] Response: %s\n", http_response.c_str());
        return false;
    }

    openai::ChatResult result = openai::parse_chat_response(http_response);
    if (!result.ok) {
        fprintf(stderr, "[error] Failed to parse OpenAI response: %s\n", result.error.c_str());
        fprintf(stderr, "[error] Raw response: %s\n", http_response.c_str());
        return false;
    }

    response = result.content;
    tool_calls = result.tool_calls;
    conversation_ = openai::append_assistant_message(conversation_with_user,
                                                     result.assistant_message_json);
    return true;
}

void OpenAIClient::add_tool_result(const char* tool_call_id, const char* result) {
    conversation_ = openai::append_tool_result(conversation_, tool_call_id, result);
}

void OpenAIClient::add_user_image_url(const char* data_url, const char* text) {
    conversation_ = openai::append_user_image_url(conversation_, data_url, text);
}

}
