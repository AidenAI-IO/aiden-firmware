#include "openrouter_codec.h"
#include "cJSON/cJSON.h"
#include <stdlib.h>

namespace aiden {
namespace openrouter {

static const char* BASE64_CHARS =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

std::string base64_encode(const uint8_t* data, size_t len) {
    std::string result;
    result.reserve((len + 2) / 3 * 4);

    for (size_t i = 0; i < len; i += 3) {
        uint32_t val = data[i] << 16;
        if (i + 1 < len) val |= data[i + 1] << 8;
        if (i + 2 < len) val |= data[i + 2];

        result += BASE64_CHARS[(val >> 18) & 0x3F];
        result += BASE64_CHARS[(val >> 12) & 0x3F];
        result += (i + 1 < len) ? BASE64_CHARS[(val >> 6) & 0x3F] : '=';
        result += (i + 2 < len) ? BASE64_CHARS[val & 0x3F] : '=';
    }

    return result;
}

static cJSON* create_tool_definitions() {
    cJSON* tools = cJSON_CreateArray();

    cJSON* kb_tap = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap, "type", "function");
    cJSON* kb_tap_func = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap_func, "name", "keyboard_tap");
    cJSON_AddStringToObject(kb_tap_func, "description", "Tap keyboard keys in sequence");
    cJSON* kb_tap_params = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap_params, "type", "object");
    cJSON* kb_tap_props = cJSON_CreateObject();
    cJSON* kb_tap_keys = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap_keys, "type", "array");
    cJSON* kb_tap_items = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap_items, "type", "string");
    cJSON_AddItemToObject(kb_tap_keys, "items", kb_tap_items);
    cJSON_AddItemToObject(kb_tap_props, "keys", kb_tap_keys);
    cJSON_AddItemToObject(kb_tap_params, "properties", kb_tap_props);
    cJSON* kb_tap_req = cJSON_CreateArray();
    cJSON_AddItemToArray(kb_tap_req, cJSON_CreateString("keys"));
    cJSON_AddItemToObject(kb_tap_params, "required", kb_tap_req);
    cJSON_AddItemToObject(kb_tap_func, "parameters", kb_tap_params);
    cJSON_AddItemToObject(kb_tap, "function", kb_tap_func);
    cJSON_AddItemToArray(tools, kb_tap);

    cJSON* kb_text = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text, "type", "function");
    cJSON* kb_text_func = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text_func, "name", "keyboard_text");
    cJSON_AddStringToObject(kb_text_func, "description", "Type text string via keyboard");
    cJSON* kb_text_params = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text_params, "type", "object");
    cJSON* kb_text_props = cJSON_CreateObject();
    cJSON* kb_text_text = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text_text, "type", "string");
    cJSON_AddItemToObject(kb_text_props, "text", kb_text_text);
    cJSON_AddItemToObject(kb_text_params, "properties", kb_text_props);
    cJSON* kb_text_req = cJSON_CreateArray();
    cJSON_AddItemToArray(kb_text_req, cJSON_CreateString("text"));
    cJSON_AddItemToObject(kb_text_params, "required", kb_text_req);
    cJSON_AddItemToObject(kb_text_func, "parameters", kb_text_params);
    cJSON_AddItemToObject(kb_text, "function", kb_text_func);
    cJSON_AddItemToArray(tools, kb_text);

    cJSON* touch_click = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_click, "type", "function");
    cJSON* touch_click_func = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_click_func, "name", "touch_click");
    cJSON_AddStringToObject(touch_click_func, "description", "Send a left click at absolute coordinates");
    cJSON* touch_click_params = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_click_params, "type", "object");
    cJSON* touch_click_props = cJSON_CreateObject();
    cJSON* touch_click_x = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_click_x, "type", "integer");
    cJSON_AddItemToObject(touch_click_props, "x", touch_click_x);
    cJSON* touch_click_y = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_click_y, "type", "integer");
    cJSON_AddItemToObject(touch_click_props, "y", touch_click_y);
    cJSON_AddItemToObject(touch_click_params, "properties", touch_click_props);
    cJSON* touch_click_req = cJSON_CreateArray();
    cJSON_AddItemToArray(touch_click_req, cJSON_CreateString("x"));
    cJSON_AddItemToArray(touch_click_req, cJSON_CreateString("y"));
    cJSON_AddItemToObject(touch_click_params, "required", touch_click_req);
    cJSON_AddItemToObject(touch_click_func, "parameters", touch_click_params);
    cJSON_AddItemToObject(touch_click, "function", touch_click_func);
    cJSON_AddItemToArray(tools, touch_click);

    cJSON* touch_swipe = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe, "type", "function");
    cJSON* touch_swipe_func = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_func, "name", "touch_swipe");
    cJSON_AddStringToObject(touch_swipe_func, "description", "Swipe on touchscreen from one point to another");
    cJSON* touch_swipe_params = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_params, "type", "object");
    cJSON* touch_swipe_props = cJSON_CreateObject();
    cJSON* touch_swipe_x1 = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_x1, "type", "integer");
    cJSON_AddItemToObject(touch_swipe_props, "x1", touch_swipe_x1);
    cJSON* touch_swipe_y1 = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_y1, "type", "integer");
    cJSON_AddItemToObject(touch_swipe_props, "y1", touch_swipe_y1);
    cJSON* touch_swipe_x2 = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_x2, "type", "integer");
    cJSON_AddItemToObject(touch_swipe_props, "x2", touch_swipe_x2);
    cJSON* touch_swipe_y2 = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_y2, "type", "integer");
    cJSON_AddItemToObject(touch_swipe_props, "y2", touch_swipe_y2);
    cJSON_AddItemToObject(touch_swipe_params, "properties", touch_swipe_props);
    cJSON* touch_swipe_req = cJSON_CreateArray();
    cJSON_AddItemToArray(touch_swipe_req, cJSON_CreateString("x1"));
    cJSON_AddItemToArray(touch_swipe_req, cJSON_CreateString("y1"));
    cJSON_AddItemToArray(touch_swipe_req, cJSON_CreateString("x2"));
    cJSON_AddItemToArray(touch_swipe_req, cJSON_CreateString("y2"));
    cJSON_AddItemToObject(touch_swipe_params, "required", touch_swipe_req);
    cJSON_AddItemToObject(touch_swipe_func, "parameters", touch_swipe_params);
    cJSON_AddItemToObject(touch_swipe, "function", touch_swipe_func);
    cJSON_AddItemToArray(tools, touch_swipe);

    return tools;
}

std::string build_tool_definitions_json() {
    cJSON* tools = create_tool_definitions();
    char* json = cJSON_PrintUnformatted(tools);
    std::string result = json ? json : "[]";
    free(json);
    cJSON_Delete(tools);
    return result;
}

ChatResult parse_chat_response(const std::string& http_response) {
    ChatResult result;
    cJSON* resp_json = cJSON_Parse(http_response.c_str());
    if (!resp_json) {
        result.error = "failed to parse response JSON";
        return result;
    }

    cJSON* choices = cJSON_GetObjectItem(resp_json, "choices");
    if (!choices || choices->type != cJSON_Array || cJSON_GetArraySize(choices) == 0) {
        result.error = "no choices in response";
        cJSON_Delete(resp_json);
        return result;
    }

    cJSON* choice = cJSON_GetArrayItem(choices, 0);
    cJSON* message = cJSON_GetObjectItem(choice, "message");
    if (!message) {
        result.error = "no message in choice";
        cJSON_Delete(resp_json);
        return result;
    }

    cJSON* content = cJSON_GetObjectItem(message, "content");
    if (content && content->type == cJSON_String)
        result.content = content->valuestring;

    cJSON* tool_calls_json = cJSON_GetObjectItem(message, "tool_calls");
    if (tool_calls_json && tool_calls_json->type == cJSON_Array) {
        int count = cJSON_GetArraySize(tool_calls_json);
        for (int i = 0; i < count; i++) {
            cJSON* tc = cJSON_GetArrayItem(tool_calls_json, i);
            cJSON* id = cJSON_GetObjectItem(tc, "id");
            cJSON* func = cJSON_GetObjectItem(tc, "function");
            if (!func) continue;

            cJSON* name = cJSON_GetObjectItem(func, "name");
            cJSON* args = cJSON_GetObjectItem(func, "arguments");

            ToolCall call;
            if (id && id->type == cJSON_String) call.id = id->valuestring;
            if (name && name->type == cJSON_String) call.name = name->valuestring;
            if (args && args->type == cJSON_String) call.arguments = args->valuestring;
            result.tool_calls.push_back(call);
        }
    }

    char* assistant_json = cJSON_PrintUnformatted(message);
    result.assistant_message_json = assistant_json ? assistant_json : "{}";
    free(assistant_json);

    result.ok = true;
    cJSON_Delete(resp_json);
    return result;
}

std::string init_conversation(const std::string& system_prompt) {
    cJSON* messages = cJSON_CreateArray();
    cJSON* sys_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(sys_msg, "role", "system");
    cJSON_AddStringToObject(sys_msg, "content", system_prompt.c_str());
    cJSON_AddItemToArray(messages, sys_msg);

    char* json = cJSON_PrintUnformatted(messages);
    std::string result = json ? json : "[]";
    free(json);
    cJSON_Delete(messages);
    return result;
}

static cJSON* parse_conversation_or_empty(const std::string& conversation_json) {
    cJSON* messages = cJSON_Parse(conversation_json.c_str());
    if (messages && messages->type == cJSON_Array) return messages;
    if (messages) cJSON_Delete(messages);
    return cJSON_CreateArray();
}

std::string append_user_audio_wav(const std::string& conversation_json,
                                  const std::string& base64_wav) {
    cJSON* messages = parse_conversation_or_empty(conversation_json);
    cJSON* user_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(user_msg, "role", "user");
    cJSON* content_array = cJSON_CreateArray();
    cJSON* audio_content = cJSON_CreateObject();
    cJSON_AddStringToObject(audio_content, "type", "input_audio");
    cJSON* input_audio = cJSON_CreateObject();
    cJSON_AddStringToObject(input_audio, "data", base64_wav.c_str());
    cJSON_AddStringToObject(input_audio, "format", "wav");
    cJSON_AddItemToObject(audio_content, "input_audio", input_audio);
    cJSON_AddItemToArray(content_array, audio_content);
    cJSON_AddItemToObject(user_msg, "content", content_array);
    cJSON_AddItemToArray(messages, user_msg);

    char* json = cJSON_PrintUnformatted(messages);
    std::string result = json ? json : "[]";
    free(json);
    cJSON_Delete(messages);
    return result;
}

std::string append_user_audio_wav_if_present(const std::string& conversation_json,
                                             const uint8_t* wav_data,
                                             size_t wav_len) {
    if (!wav_data || wav_len == 0)
        return conversation_json;
    return append_user_audio_wav(conversation_json, base64_encode(wav_data, wav_len));
}

std::string append_user_text(const std::string& conversation_json,
                             const char* text) {
    const char* safe = text ? text : "";
    cJSON* messages = parse_conversation_or_empty(conversation_json);
    cJSON* user_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(user_msg, "role", "user");
    cJSON_AddStringToObject(user_msg, "content", safe);
    cJSON_AddItemToArray(messages, user_msg);

    char* json = cJSON_PrintUnformatted(messages);
    std::string result = json ? json : "[]";
    free(json);
    cJSON_Delete(messages);
    return result;
}

std::string append_assistant_message(const std::string& conversation_json,
                                     const std::string& assistant_message_json) {
    cJSON* messages = parse_conversation_or_empty(conversation_json);
    cJSON* message = cJSON_Parse(assistant_message_json.c_str());
    if (!message) {
        cJSON_Delete(messages);
        return conversation_json;
    }

    cJSON_AddItemToArray(messages, message);
    char* json = cJSON_PrintUnformatted(messages);
    std::string result = json ? json : conversation_json;
    free(json);
    cJSON_Delete(messages);
    return result;
}

std::string append_tool_result(const std::string& conversation_json,
                               const std::string& tool_call_id,
                               const std::string& result_text) {
    cJSON* messages = parse_conversation_or_empty(conversation_json);
    cJSON* tool_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(tool_msg, "role", "tool");
    cJSON_AddStringToObject(tool_msg, "tool_call_id", tool_call_id.c_str());
    cJSON_AddStringToObject(tool_msg, "content", result_text.c_str());
    cJSON_AddItemToArray(messages, tool_msg);

    char* json = cJSON_PrintUnformatted(messages);
    std::string result = json ? json : conversation_json;
    free(json);
    cJSON_Delete(messages);
    return result;
}

std::string build_chat_request(const std::string& conversation_json,
                               const std::string& model) {
    cJSON* messages = parse_conversation_or_empty(conversation_json);
    cJSON* request = cJSON_CreateObject();
    cJSON_AddStringToObject(request, "model", model.c_str());
    cJSON_AddItemToObject(request, "messages", messages);
    cJSON_AddItemToObject(request, "tools", create_tool_definitions());

    char* json = cJSON_PrintUnformatted(request);
    std::string result = json ? json : "{}";
    free(json);
    cJSON_Delete(request);
    return result;
}

}
}
