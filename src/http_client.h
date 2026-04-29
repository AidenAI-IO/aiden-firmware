#pragma once
#include <string>
#include <vector>
#include <stddef.h>

namespace aiden {

typedef void (*StreamChunkCallback)(const char* data, size_t len, void* user_data);

class HttpClient {
public:
    bool is_available();

    bool post_json(const char* url, const char* auth_header,
                   const char* json_body, std::string& response);

    bool post_binary(const char* url, const char* auth_header,
                     const char* json_body, std::vector<uint8_t>& response);

    bool post_stream(const char* url, const char* auth_header,
                     const char* json_body, StreamChunkCallback callback,
                     void* user_data);
};

}
