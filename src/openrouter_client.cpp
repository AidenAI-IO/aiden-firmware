#include "openrouter_client.h"
#include "http_client.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <stdlib.h>

namespace aiden {

static const char* BASE64_CHARS =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static std::string base64_encode(const uint8_t* data, size_t len) {
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

    // keyboard_tap tool
    cJSON* kb_tap = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap, "type", "function");
    cJSON* kb_tap_func = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_tap_func, "name", "keyboard_tap");
    cJSON_AddStringToObject(kb_tap_func, "description",
        "Tap keyboard keys in sequence");
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

    // keyboard_text tool
    cJSON* kb_text = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text, "type", "function");
    cJSON* kb_text_func = cJSON_CreateObject();
    cJSON_AddStringToObject(kb_text_func, "name", "keyboard_text");
    cJSON_AddStringToObject(kb_text_func, "description",
        "Type text string via keyboard");
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

    // touch_tap tool
    cJSON* touch_tap = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_tap, "type", "function");
    cJSON* touch_tap_func = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_tap_func, "name", "touch_tap");
    cJSON_AddStringToObject(touch_tap_func, "description",
        "Tap touchscreen at coordinates");
    cJSON* touch_tap_params = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_tap_params, "type", "object");
    cJSON* touch_tap_props = cJSON_CreateObject();
    cJSON* touch_tap_x = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_tap_x, "type", "integer");
    cJSON_AddItemToObject(touch_tap_props, "x", touch_tap_x);
    cJSON* touch_tap_y = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_tap_y, "type", "integer");
    cJSON_AddItemToObject(touch_tap_props, "y", touch_tap_y);
    cJSON_AddItemToObject(touch_tap_params, "properties", touch_tap_props);
    cJSON* touch_tap_req = cJSON_CreateArray();
    cJSON_AddItemToArray(touch_tap_req, cJSON_CreateString("x"));
    cJSON_AddItemToArray(touch_tap_req, cJSON_CreateString("y"));
    cJSON_AddItemToObject(touch_tap_params, "required", touch_tap_req);
    cJSON_AddItemToObject(touch_tap_func, "parameters", touch_tap_params);
    cJSON_AddItemToObject(touch_tap, "function", touch_tap_func);
    cJSON_AddItemToArray(tools, touch_tap);

    // touch_swipe tool
    cJSON* touch_swipe = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe, "type", "function");
    cJSON* touch_swipe_func = cJSON_CreateObject();
    cJSON_AddStringToObject(touch_swipe_func, "name", "touch_swipe");
    cJSON_AddStringToObject(touch_swipe_func, "description",
        "Swipe on touchscreen from one point to another");
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

OpenRouterClient::OpenRouterClient(const char* api_key, const char* llm_model,
                                   const char* tts_model)
    : api_key_(api_key), llm_model_(llm_model), tts_model_(tts_model) {

    // Initialize conversation with system message
    cJSON* messages = cJSON_CreateArray();
    cJSON* sys_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(sys_msg, "role", "system");
    cJSON_AddStringToObject(sys_msg, "content",
        "You are an AI assistant controlling a device via keyboard and touchscreen. "
        "Use the provided tools to interact. Touch coordinates: 0-32767 absolute range.");
    cJSON_AddItemToArray(messages, sys_msg);

    char* json_str = cJSON_PrintUnformatted(messages);
    conversation_ = json_str;
    free(json_str);
    cJSON_Delete(messages);
}

bool OpenRouterClient::chat(const uint8_t* wav_data, size_t wav_len,
                            std::string& response, std::vector<ToolCall>& tool_calls) {
    // Base64 encode the WAV data
    std::string b64_audio = base64_encode(wav_data, wav_len);

    // Parse current conversation
    cJSON* messages = cJSON_Parse(conversation_.c_str());
    if (!messages) {
        fprintf(stderr, "Failed to parse conversation JSON\n");
        return false;
    }

    // Add user message with audio
    cJSON* user_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(user_msg, "role", "user");
    cJSON* content_array = cJSON_CreateArray();
    cJSON* audio_content = cJSON_CreateObject();
    cJSON_AddStringToObject(audio_content, "type", "input_audio");
    cJSON* input_audio = cJSON_CreateObject();
    cJSON_AddStringToObject(input_audio, "data", b64_audio.c_str());
    cJSON_AddStringToObject(input_audio, "format", "wav");
    cJSON_AddItemToObject(audio_content, "input_audio", input_audio);
    cJSON_AddItemToArray(content_array, audio_content);
    cJSON_AddItemToObject(user_msg, "content", content_array);
    cJSON_AddItemToArray(messages, user_msg);

    // Build request JSON
    cJSON* request = cJSON_CreateObject();
    cJSON_AddStringToObject(request, "model", llm_model_.c_str());
    cJSON_AddItemToObject(request, "messages", cJSON_Duplicate(messages, 1));
    cJSON_AddItemToObject(request, "tools", create_tool_definitions());

    char* request_str = cJSON_PrintUnformatted(request);
    cJSON_Delete(request);

    // Make HTTP request
    HttpClient http;
    std::string http_response;
    bool success = http.post_json(
        "https://openrouter.ai/api/v1/chat/completions",
        api_key_.c_str(),
        request_str,
        http_response);
    free(request_str);

    if (!success) {
        fprintf(stderr, "[error] HTTP request failed\n");
        fprintf(stderr, "[error] Response: %s\n", http_response.c_str());
        cJSON_Delete(messages);
        return false;
    }

    // Parse response
    cJSON* resp_json = cJSON_Parse(http_response.c_str());
    if (!resp_json) {
        fprintf(stderr, "[error] Failed to parse response JSON\n");
        fprintf(stderr, "[error] Raw response: %s\n", http_response.c_str());
        cJSON_Delete(messages);
        return false;
    }

    cJSON* choices = cJSON_GetObjectItem(resp_json, "choices");
    if (!choices || choices->type != cJSON_Array || cJSON_GetArraySize(choices) == 0) {
        char* pretty = cJSON_Print(resp_json);
        fprintf(stderr, "[error] No choices in response:\n%s\n", pretty ? pretty : http_response.c_str());
        free(pretty);
        cJSON_Delete(resp_json);
        cJSON_Delete(messages);
        return false;
    }

    cJSON* choice = cJSON_GetArrayItem(choices, 0);
    cJSON* message = cJSON_GetObjectItem(choice, "message");
    if (!message) {
        fprintf(stderr, "No message in choice\n");
        cJSON_Delete(resp_json);
        cJSON_Delete(messages);
        return false;
    }

    // Extract content
    cJSON* content = cJSON_GetObjectItem(message, "content");
    if (content && content->type == cJSON_String) {
        response = content->valuestring;
    } else {
        response.clear();
    }

    // Extract tool calls
    tool_calls.clear();
    cJSON* tool_calls_json = cJSON_GetObjectItem(message, "tool_calls");
    if (tool_calls_json && tool_calls_json->type == cJSON_Array) {
        int count = cJSON_GetArraySize(tool_calls_json);
        for (int i = 0; i < count; i++) {
            cJSON* tc = cJSON_GetArrayItem(tool_calls_json, i);
            cJSON* id = cJSON_GetObjectItem(tc, "id");
            cJSON* func = cJSON_GetObjectItem(tc, "function");
            if (func) {
                cJSON* name = cJSON_GetObjectItem(func, "name");
                cJSON* args = cJSON_GetObjectItem(func, "arguments");

                ToolCall call;
                if (id && id->type == cJSON_String) call.id = id->valuestring;
                if (name && name->type == cJSON_String) call.name = name->valuestring;
                if (args && args->type == cJSON_String) call.arguments = args->valuestring;

                tool_calls.push_back(call);
            }
        }
    }

    // Append assistant message to conversation
    cJSON_AddItemToArray(messages, cJSON_Duplicate(message, 1));
    char* conv_str = cJSON_PrintUnformatted(messages);
    conversation_ = conv_str;
    free(conv_str);

    cJSON_Delete(resp_json);
    cJSON_Delete(messages);
    return true;
}

void OpenRouterClient::add_tool_result(const char* tool_call_id, const char* result) {
    cJSON* messages = cJSON_Parse(conversation_.c_str());
    if (!messages) {
        fprintf(stderr, "Failed to parse conversation for tool result\n");
        return;
    }

    cJSON* tool_msg = cJSON_CreateObject();
    cJSON_AddStringToObject(tool_msg, "role", "tool");
    cJSON_AddStringToObject(tool_msg, "tool_call_id", tool_call_id);
    cJSON_AddStringToObject(tool_msg, "content", result);
    cJSON_AddItemToArray(messages, tool_msg);

    char* conv_str = cJSON_PrintUnformatted(messages);
    conversation_ = conv_str;
    free(conv_str);
    cJSON_Delete(messages);
}

bool OpenRouterClient::text_to_speech(const char* text, std::vector<uint8_t>& pcm_audio) {
    cJSON* request = cJSON_CreateObject();
    cJSON_AddStringToObject(request, "model", tts_model_.c_str());
    cJSON_AddStringToObject(request, "voice", "alloy");
    cJSON_AddStringToObject(request, "input", text);
    cJSON_AddStringToObject(request, "response_format", "mp3");

    char* request_str = cJSON_PrintUnformatted(request);
    fprintf(stderr, "[tts] Request: %s\n", request_str);
    cJSON_Delete(request);

    HttpClient http;
    std::vector<uint8_t> mp3_data;
    bool success = http.post_binary(
        "https://openrouter.ai/api/v1/audio/speech",
        api_key_.c_str(),
        request_str,
        mp3_data);
    free(request_str);

    if (!success) {
        fprintf(stderr, "[error] TTS HTTP request failed\n");
        return false;
    }

    // Check if response is a JSON error instead of MP3 audio
    if (!mp3_data.empty() && mp3_data[0] == '{') {
        std::string err_str(mp3_data.begin(), mp3_data.end());
        fprintf(stderr, "[error] TTS returned error: %s\n", err_str.c_str());
        return false;
    }

    fprintf(stderr, "[tts] Received %zu bytes of MP3 audio\n", mp3_data.size());

    // Decode MP3 to PCM using ffmpeg
    char mp3_file[] = "/tmp/agent_tts_XXXXXX.mp3";
    int fd = mkstemps(mp3_file, 4);
    if (fd < 0) {
        fprintf(stderr, "[error] Failed to create temp MP3 file\n");
        return false;
    }
    write(fd, mp3_data.data(), mp3_data.size());
    close(fd);

    char pcm_file[] = "/tmp/agent_pcm_XXXXXX.pcm";
    fd = mkstemps(pcm_file, 4);
    if (fd < 0) {
        unlink(mp3_file);
        fprintf(stderr, "[error] Failed to create temp PCM file\n");
        return false;
    }
    close(fd);

    // Convert MP3 to 16kHz mono 16-bit PCM
    char cmd[512];
    snprintf(cmd, sizeof(cmd),
        "ffmpeg -i '%s' -f s16le -ar 16000 -ac 1 '%s' 2>/dev/null",
        mp3_file, pcm_file);

    fprintf(stderr, "[tts] Decoding MP3 to PCM...\n");
    int status = system(cmd);
    unlink(mp3_file);

    if (status != 0) {
        unlink(pcm_file);
        fprintf(stderr, "[error] ffmpeg failed (status %d). Is ffmpeg installed?\n", status);
        return false;
    }

    // Read PCM file
    FILE* fp = fopen(pcm_file, "rb");
    if (!fp) {
        unlink(pcm_file);
        fprintf(stderr, "[error] Failed to open PCM file\n");
        return false;
    }

    fseek(fp, 0, SEEK_END);
    long size = ftell(fp);
    fseek(fp, 0, SEEK_SET);

    pcm_audio.resize(size);
    fread(pcm_audio.data(), 1, size, fp);
    fclose(fp);
    unlink(pcm_file);

    fprintf(stderr, "[tts] Decoded to %zu bytes of PCM audio\n", pcm_audio.size());
    return true;
}

}
