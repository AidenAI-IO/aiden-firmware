#pragma once
#include <string>

namespace aiden {

struct ToolCommandResult {
    bool ok;
    std::string command;
    std::string error;

    ToolCommandResult() : ok(false) {}
};

ToolCommandResult build_tool_command(const char* hid_binary,
                                     const char* tool_name,
                                     const char* args_json);

bool is_frame_tool(const char* tool_name);
std::string handle_frame_tool(const char* socket_path,
                              const char* tool_name,
                              const char* args_json,
                              const char* output_dir);

}
