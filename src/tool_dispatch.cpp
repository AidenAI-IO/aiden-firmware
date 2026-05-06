#include "tool_dispatch.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <string.h>

namespace aiden {

static std::string shell_single_quote(const char* text) {
    std::string out = "'";
    for (const char* p = text; *p; ++p) {
        if (*p == '\'') out += "'\\''";
        else out += *p;
    }
    out += "'";
    return out;
}

ToolCommandResult build_tool_command(const char* hid_binary,
                                     const char* tool_name,
                                     const char* args_json) {
    ToolCommandResult result;
    cJSON* args = cJSON_Parse(args_json);
    if (!args) {
        result.error = "error: invalid JSON";
        return result;
    }

    char cmd[1024];

    if (strcmp(tool_name, "keyboard_tap") == 0) {
        cJSON* keys = cJSON_GetObjectItem(args, "keys");
        if (!keys || keys->type != cJSON_Array) {
            cJSON_Delete(args);
            result.error = "error: missing keys array";
            return result;
        }

        int len = snprintf(cmd, sizeof(cmd), "sudo %s keyboard tap", hid_binary);
        int count = cJSON_GetArraySize(keys);
        for (int i = 0; i < count && len < (int)sizeof(cmd) - 32; i++) {
            cJSON* key = cJSON_GetArrayItem(keys, i);
            if (key && key->type == cJSON_String)
                len += snprintf(cmd + len, sizeof(cmd) - len, " %s", key->valuestring);
        }
        result.command = cmd;
    }
    else if (strcmp(tool_name, "keyboard_text") == 0) {
        cJSON* text = cJSON_GetObjectItem(args, "text");
        if (!text || text->type != cJSON_String) {
            cJSON_Delete(args);
            result.error = "error: missing text";
            return result;
        }

        std::string quoted = shell_single_quote(text->valuestring);
        snprintf(cmd, sizeof(cmd), "sudo %s keyboard text %s", hid_binary, quoted.c_str());
        result.command = cmd;
    }
    else if (strcmp(tool_name, "touch_click") == 0) {
        cJSON* x = cJSON_GetObjectItem(args, "x");
        cJSON* y = cJSON_GetObjectItem(args, "y");
        if (!x || !y) {
            cJSON_Delete(args);
            result.error = "error: missing x or y";
            return result;
        }

        snprintf(cmd, sizeof(cmd), "sudo %s touch click %d %d",
                 hid_binary, x->valueint, y->valueint);
        result.command = cmd;
    }
    else if (strcmp(tool_name, "touch_swipe") == 0) {
        cJSON* x1 = cJSON_GetObjectItem(args, "x1");
        cJSON* y1 = cJSON_GetObjectItem(args, "y1");
        cJSON* x2 = cJSON_GetObjectItem(args, "x2");
        cJSON* y2 = cJSON_GetObjectItem(args, "y2");
        if (!x1 || !y1 || !x2 || !y2) {
            cJSON_Delete(args);
            result.error = "error: missing coordinates";
            return result;
        }

        snprintf(cmd, sizeof(cmd),
                 "sudo %s touch down %d %d && sudo %s touch move %d %d && sudo %s touch up",
                 hid_binary, x1->valueint, y1->valueint,
                 hid_binary, x2->valueint, y2->valueint,
                 hid_binary);
        result.command = cmd;
    }
    else {
        cJSON_Delete(args);
        result.error = "error: unknown tool";
        return result;
    }

    cJSON_Delete(args);
    result.ok = true;
    return result;
}

}
