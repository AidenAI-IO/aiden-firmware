#include "doctest.h"
#include "openai_codec.h"
#include "cJSON/cJSON.h"
#include <string>

using aiden::openai::build_chat_request;
using aiden::openai::parse_chat_response;
using aiden::openai::init_conversation;
using aiden::openai::append_user_audio_wav;
using aiden::openai::append_user_audio_wav_if_present;
using aiden::openai::append_user_text;
using aiden::openai::append_assistant_message;
using aiden::openai::append_tool_result;
using aiden::openai::ChatResult;

TEST_CASE("openai build_chat_request wraps conversation with model and tools") {
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

TEST_CASE("openai parse_chat_response extracts plain text content") {
    std::string resp = R"({"choices":[{"message":{"role":"assistant","content":"hi there"}}]})";
    ChatResult r = parse_chat_response(resp);
    REQUIRE(r.ok);
    CHECK(r.content == "hi there");
    CHECK(r.tool_calls.empty());
    CHECK_FALSE(r.assistant_message_json.empty());
}

TEST_CASE("openai parse_chat_response extracts tool_calls") {
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

TEST_CASE("openai append_user_audio_wav appends a user message with input_audio") {
    std::string conv = init_conversation("sys");
    std::string appended = append_user_audio_wav(conv, "AAAA");

    cJSON* arr = cJSON_Parse(appended.c_str());
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

TEST_CASE("openai append_user_audio_wav_if_present skips empty follow-up audio") {
    std::string conv = init_conversation("sys");
    std::string unchanged = append_user_audio_wav_if_present(conv, NULL, 0);
    CHECK(unchanged == conv);
}

TEST_CASE("openai append_user_text appends a user message with string content") {
    std::string conv = init_conversation("sys");
    std::string appended = append_user_text(conv, "hello world");

    cJSON* arr = cJSON_Parse(appended.c_str());
    REQUIRE(arr->type == cJSON_Array);
    REQUIRE(cJSON_GetArraySize(arr) == 2);

    cJSON* user = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(user, "role")->valuestring) == "user");
    cJSON* content = cJSON_GetObjectItem(user, "content");
    REQUIRE(content->type == cJSON_String);
    CHECK(std::string(content->valuestring) == "hello world");
    cJSON_Delete(arr);
}

TEST_CASE("openai append_user_text escapes quotes and survives NULL") {
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

TEST_CASE("openai append_tool_result adds a role=tool message") {
    std::string conv = init_conversation("sys");
    conv = append_tool_result(conv, "call_abc", "ok");

    cJSON* arr = cJSON_Parse(conv.c_str());
    cJSON* tool = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(tool, "role")->valuestring) == "tool");
    CHECK(std::string(cJSON_GetObjectItem(tool, "tool_call_id")->valuestring) == "call_abc");
    CHECK(std::string(cJSON_GetObjectItem(tool, "content")->valuestring) == "ok");
    cJSON_Delete(arr);
}

TEST_CASE("openai append_assistant_message appends verbatim message JSON") {
    std::string conv = init_conversation("sys");
    std::string assistant = R"({"role":"assistant","content":"hello"})";
    conv = append_assistant_message(conv, assistant);

    cJSON* arr = cJSON_Parse(conv.c_str());
    REQUIRE(cJSON_GetArraySize(arr) == 2);
    cJSON* msg = cJSON_GetArrayItem(arr, 1);
    CHECK(std::string(cJSON_GetObjectItem(msg, "role")->valuestring) == "assistant");
    CHECK(std::string(cJSON_GetObjectItem(msg, "content")->valuestring) == "hello");
    cJSON_Delete(arr);
}
