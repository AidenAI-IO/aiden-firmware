#include "doctest.h"
#include "openrouter_codec.h"
#include "cJSON/cJSON.h"
#include <string>
#include <cstring>

using aiden::openrouter::base64_encode;
using aiden::openrouter::build_tool_definitions_json;
using aiden::openrouter::parse_chat_response;
using aiden::openrouter::init_conversation;
using aiden::openrouter::append_user_audio_wav;
using aiden::openrouter::append_user_audio_wav_if_present;
using aiden::openrouter::append_user_text;
using aiden::openrouter::append_user_image_url;
using aiden::openrouter::append_assistant_message;
using aiden::openrouter::append_tool_result;
using aiden::openrouter::build_chat_request;
using aiden::openrouter::ChatResult;

TEST_CASE("base64_encode handles canonical RFC 4648 cases") {
    auto enc = [](const char* s) {
        return base64_encode(reinterpret_cast<const uint8_t*>(s), std::strlen(s));
    };
    CHECK(enc("") == "");
    CHECK(enc("f") == "Zg==");
    CHECK(enc("fo") == "Zm8=");
    CHECK(enc("foo") == "Zm9v");
    CHECK(enc("foob") == "Zm9vYg==");
    CHECK(enc("fooba") == "Zm9vYmE=");
    CHECK(enc("foobar") == "Zm9vYmFy");
}

TEST_CASE("base64_encode handles binary bytes") {
    uint8_t bytes[] = {0x00, 0xFF, 0x10, 0xAB};
    CHECK(base64_encode(bytes, sizeof(bytes)) == "AP8Qqw==");
}

TEST_CASE("build_tool_definitions_json contains HID and frame tools") {
    std::string json = build_tool_definitions_json();
    cJSON* arr = cJSON_Parse(json.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    CHECK(cJSON_GetArraySize(arr) == 7);

    std::string names;
    for (cJSON* item = arr->child; item; item = item->next) {
        cJSON* func = cJSON_GetObjectItem(item, "function");
        REQUIRE(func != nullptr);
        cJSON* name = cJSON_GetObjectItem(func, "name");
        REQUIRE(name != nullptr);
        REQUIRE(name->type == cJSON_String);
        names += name->valuestring;
        names += ",";
    }
    cJSON_Delete(arr);

    CHECK(names.find("keyboard_tap,") != std::string::npos);
    CHECK(names.find("keyboard_text,") != std::string::npos);
    CHECK(names.find("touch_click,") != std::string::npos);
    CHECK(names.find("touch_swipe,") != std::string::npos);
    CHECK(names.find("capture_screenshot,") != std::string::npos);
    CHECK(names.find("frame_service_health,") != std::string::npos);
    CHECK(names.find("frame_service_restart,") != std::string::npos);
}

TEST_CASE("parse_chat_response extracts plain text content") {
    std::string resp = R"({"choices":[{"message":{"role":"assistant","content":"hi there"}}]})";
    ChatResult r = parse_chat_response(resp);
    REQUIRE(r.ok);
    CHECK(r.content == "hi there");
    CHECK(r.tool_calls.empty());
    CHECK_FALSE(r.assistant_message_json.empty());
}

TEST_CASE("parse_chat_response extracts tool_calls") {
    std::string resp = R"({
      "choices":[{"message":{
        "role":"assistant",
        "content":null,
        "tool_calls":[
          {"id":"call_1","function":{"name":"touch_click","arguments":"{\"x\":100,\"y\":200}"}},
          {"id":"call_2","function":{"name":"keyboard_text","arguments":"{\"text\":\"hi\"}"}}
        ]
      }}]
    })";
    ChatResult r = parse_chat_response(resp);
    REQUIRE(r.ok);
    CHECK(r.content.empty());
    REQUIRE(r.tool_calls.size() == 2);
    CHECK(r.tool_calls[0].id == "call_1");
    CHECK(r.tool_calls[0].name == "touch_click");
    CHECK(r.tool_calls[0].arguments == R"({"x":100,"y":200})");
    CHECK(r.tool_calls[1].id == "call_2");
    CHECK(r.tool_calls[1].name == "keyboard_text");
}

TEST_CASE("parse_chat_response rejects malformed JSON") {
    ChatResult r = parse_chat_response("not json {{{");
    CHECK_FALSE(r.ok);
    CHECK_FALSE(r.error.empty());
}

TEST_CASE("parse_chat_response rejects response with no choices") {
    ChatResult r = parse_chat_response(R"({"error":{"message":"server boom"}})");
    CHECK_FALSE(r.ok);
}

TEST_CASE("parse_chat_response rejects empty choices array") {
    ChatResult r = parse_chat_response(R"({"choices":[]})");
    CHECK_FALSE(r.ok);
}

TEST_CASE("init_conversation embeds the system prompt") {
    std::string conv = init_conversation("You are a helpful assistant.");
    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 1);

    cJSON* msg = cJSON_GetArrayItem(arr, 0);
    cJSON* role = cJSON_GetObjectItem(msg, "role");
    cJSON* content = cJSON_GetObjectItem(msg, "content");
    REQUIRE(role != nullptr);
    REQUIRE(content != nullptr);
    REQUIRE(role->type == cJSON_String);
    REQUIRE(content->type == cJSON_String);
    CHECK(std::string(role->valuestring) == "system");
    CHECK(std::string(content->valuestring) == "You are a helpful assistant.");
    cJSON_Delete(arr);
}

TEST_CASE("append_user_audio_wav appends a user message with input_audio") {
    std::string conv = init_conversation("sys");
    std::string appended = append_user_audio_wav(conv, "AAAA");

    cJSON* arr = cJSON_Parse(appended.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 2);

    cJSON* user = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(user, "role")->valuestring) == "user");
    cJSON* content = cJSON_GetObjectItem(user, "content");
    REQUIRE(content->type == cJSON_Array);
    cJSON* first = cJSON_GetArrayItem(content, 0);
    CHECK(std::string(cJSON_GetObjectItem(first, "type")->valuestring) == "input_audio");
    cJSON* ia = cJSON_GetObjectItem(first, "input_audio");
    CHECK(std::string(cJSON_GetObjectItem(ia, "data")->valuestring) == "AAAA");
    CHECK(std::string(cJSON_GetObjectItem(ia, "format")->valuestring) == "wav");
    cJSON_Delete(arr);
}

TEST_CASE("append_user_audio_wav_if_present skips empty follow-up audio") {
    std::string conv = init_conversation("sys");
    std::string unchanged = append_user_audio_wav_if_present(conv, NULL, 0);
    CHECK(unchanged == conv);
}

TEST_CASE("openrouter append_user_text appends a user message with string content") {
    std::string conv = init_conversation("sys");
    std::string appended = append_user_text(conv, "hello world");

    cJSON* arr = cJSON_Parse(appended.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 2);

    cJSON* user = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(user, "role")->valuestring) == "user");
    cJSON* content = cJSON_GetObjectItem(user, "content");
    REQUIRE(content->type == cJSON_String);
    CHECK(std::string(content->valuestring) == "hello world");
    cJSON_Delete(arr);
}

TEST_CASE("openrouter append_user_text escapes quotes and survives NULL") {
    std::string conv = init_conversation("sys");
    std::string quoted = append_user_text(conv, "say \"hi\" now");
    cJSON* arr = cJSON_Parse(quoted.c_str());
    REQUIRE(arr != nullptr);
    cJSON* user = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(user, "content")->valuestring) == "say \"hi\" now");
    cJSON_Delete(arr);

    std::string null_input = append_user_text(conv, NULL);
    cJSON* arr2 = cJSON_Parse(null_input.c_str());
    REQUIRE(arr2 != nullptr);
    cJSON* user2 = cJSON_GetArrayItem(arr2, 1);
    CHECK(std::string(cJSON_GetObjectItem(user2, "content")->valuestring) == "");
    cJSON_Delete(arr2);
}

TEST_CASE("openrouter append_user_image_url appends vision content") {
    std::string conv = init_conversation("sys");
    std::string appended = append_user_image_url(conv,
                                                 "data:image/png;base64,AAAA",
                                                 "Screenshot captured from frame_service.");

    cJSON* arr = cJSON_Parse(appended.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(cJSON_GetArraySize(arr) == 2);
    cJSON* user = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(user, "role")->valuestring) == "user");
    cJSON* content = cJSON_GetObjectItem(user, "content");
    REQUIRE(content != nullptr);
    REQUIRE(content->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(content) == 2);
    cJSON* text = cJSON_GetArrayItem(content, 0);
    CHECK(std::string(cJSON_GetObjectItem(text, "type")->valuestring) == "text");
    CHECK(std::string(cJSON_GetObjectItem(text, "text")->valuestring) == "Screenshot captured from frame_service.");
    cJSON* image = cJSON_GetArrayItem(content, 1);
    CHECK(std::string(cJSON_GetObjectItem(image, "type")->valuestring) == "image_url");
    cJSON* image_url = cJSON_GetObjectItem(image, "image_url");
    REQUIRE(image_url != nullptr);
    CHECK(std::string(cJSON_GetObjectItem(image_url, "url")->valuestring) == "data:image/png;base64,AAAA");
    cJSON_Delete(arr);
}

TEST_CASE("append_tool_result adds a role=tool message") {
    std::string conv = init_conversation("sys");
    conv = append_tool_result(conv, "call_abc", "ok");

    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 2);
    cJSON* tool = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(tool, "role")->valuestring) == "tool");
    CHECK(std::string(cJSON_GetObjectItem(tool, "tool_call_id")->valuestring) == "call_abc");
    CHECK(std::string(cJSON_GetObjectItem(tool, "content")->valuestring) == "ok");
    cJSON_Delete(arr);
}

TEST_CASE("append_assistant_message appends verbatim message JSON") {
    std::string conv = init_conversation("sys");
    std::string assistant = R"({"role":"assistant","content":"hello"})";
    conv = append_assistant_message(conv, assistant);

    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 2);
    cJSON* msg = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(msg, "role")->valuestring) == "assistant");
    CHECK(std::string(cJSON_GetObjectItem(msg, "content")->valuestring) == "hello");
    cJSON_Delete(arr);
}

TEST_CASE("build_chat_request wraps conversation with model and tools") {
    std::string conv = init_conversation("sys");
    std::string req = build_chat_request(conv, "gpt-4o-audio-preview");

    cJSON* obj = cJSON_Parse(req.c_str());
    REQUIRE(obj != nullptr);
    cJSON* model = cJSON_GetObjectItem(obj, "model");
    REQUIRE(model != nullptr);
    REQUIRE(model->type == cJSON_String);
    CHECK(std::string(model->valuestring) == "gpt-4o-audio-preview");
    CHECK(cJSON_GetObjectItem(obj, "messages")->type == cJSON_Array);
    CHECK(cJSON_GetObjectItem(obj, "tools")->type == cJSON_Array);
    cJSON_Delete(obj);
}

TEST_CASE("parse_chat_response tolerates tool_calls with missing fields") {
    std::string resp = R"({
      "choices":[{"message":{
        "role":"assistant",
        "tool_calls":[
          {"id":"call_1","function":{"arguments":"{}"}},
          {"function":{"name":"keyboard_text"}},
          {"id":"call_3"}
        ]
      }}]
    })";

    ChatResult r = parse_chat_response(resp);
    REQUIRE(r.ok);
    REQUIRE(r.tool_calls.size() == 2);
    CHECK(r.tool_calls[0].id == "call_1");
    CHECK(r.tool_calls[0].name.empty());
    CHECK(r.tool_calls[0].arguments == "{}");
    CHECK(r.tool_calls[1].id.empty());
    CHECK(r.tool_calls[1].name == "keyboard_text");
}

TEST_CASE("append_user_audio_wav recovers from malformed conversation input") {
    std::string conv = append_user_audio_wav("not-json", "BBBB");
    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 1);

    cJSON* user = cJSON_GetArrayItem(arr, 0);
    CHECK(std::string(cJSON_GetObjectItem(user, "role")->valuestring) == "user");
    cJSON_Delete(arr);
}

TEST_CASE("append_tool_result recovers from malformed conversation input") {
    std::string conv = append_tool_result("not-json", "call_xyz", "done");
    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(arr != nullptr);
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 1);

    cJSON* tool = cJSON_GetArrayItem(arr, 0);
    CHECK(std::string(cJSON_GetObjectItem(tool, "role")->valuestring) == "tool");
    CHECK(std::string(cJSON_GetObjectItem(tool, "tool_call_id")->valuestring) == "call_xyz");
    cJSON_Delete(arr);
}

TEST_CASE("append_assistant_message leaves conversation unchanged on invalid assistant JSON") {
    std::string conv = init_conversation("sys");
    std::string unchanged = append_assistant_message(conv, "{broken-json");
    CHECK(unchanged == conv);
}

TEST_CASE("build_chat_request preserves prior conversation messages") {
    std::string conv = init_conversation("sys");
    conv = append_user_audio_wav(conv, "AAAA");
    conv = append_tool_result(conv, "call_1", "ok");
    std::string req = build_chat_request(conv, "model-x");

    cJSON* obj = cJSON_Parse(req.c_str());
    REQUIRE(obj != nullptr);
    cJSON* messages = cJSON_GetObjectItem(obj, "messages");
    REQUIRE(messages != nullptr);
    REQUIRE(messages->type == cJSON_Array);
    CHECK(cJSON_GetArraySize(messages) == 3);
    cJSON_Delete(obj);
}
