#include "tool_dispatch.h"
#include "cJSON/cJSON.h"
#include "frame_processing.h"
#include "frame_service_client.h"
#include "frame_service_defaults.h"
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

namespace aiden {

static std::string json_escape(const std::string& text) {
    std::string out;
    for (size_t i = 0; i < text.size(); ++i) {
        char c = text[i];
        if (c == '\\' || c == '"') out.push_back('\\');
        out.push_back(c);
    }
    return out;
}

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
            if (key && key->type == cJSON_String) {
                std::string quoted = shell_single_quote(key->valuestring);
                len += snprintf(cmd + len, sizeof(cmd) - len, " %s", quoted.c_str());
            }
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

bool is_frame_tool(const char* tool_name) {
    return tool_name &&
           (strcmp(tool_name, "capture_screenshot") == 0 ||
            strcmp(tool_name, "frame_service_health") == 0 ||
            strcmp(tool_name, "frame_service_restart") == 0);
}

static std::string frame_tool_error(FrameServiceStatus status) {
    return std::string("{\"ok\":false,\"status\":\"") +
           frame_service_status_to_string(status) + "\"}";
}

static bool write_file(const std::string& path, const std::vector<uint8_t>& bytes) {
    FILE* fp = fopen(path.c_str(), "wb");
    if (!fp) return false;
    size_t written = fwrite(bytes.data(), 1, bytes.size(), fp);
    fclose(fp);
    return written == bytes.size();
}

static std::string u64_text(uint64_t value) {
    char text[32];
    snprintf(text, sizeof(text), "%llu", static_cast<unsigned long long>(value));
    return text;
}

static std::string health_json(const HealthResult& health) {
    return std::string("{\"ok\":true") +
           ",\"state\":\"" + json_escape(health.state) + "\"" +
           ",\"latest_seq\":\"" + u64_text(health.latest_seq) + "\"" +
           ",\"frame_age_ms\":\"" + u64_text(health.frame_age_ms) + "\"" +
           ",\"ring_buffer_size\":" + std::to_string(health.ring_buffer_size) +
           ",\"ring_buffer_used\":" + std::to_string(health.ring_buffer_used) +
           ",\"consecutive_failures\":" + std::to_string(health.consecutive_failures) +
           ",\"last_error\":\"" + json_escape(health.last_error) + "\"" +
           ",\"last_recovery_ts\":\"" + u64_text(health.last_recovery_ts) + "\"" +
           ",\"avg_frame_serve_latency_ms\":" + std::to_string(health.avg_frame_serve_latency_ms) +
           ",\"avg_capture_copy_latency_ms\":" + std::to_string(health.avg_capture_copy_latency_ms) +
           "}";
}

static uint32_t json_u32(cJSON* object, const char* key, uint32_t fallback) {
    cJSON* item = cJSON_GetObjectItem(object, key);
    if (!item) return fallback;
    if (item->type != cJSON_Number || item->valueint < 0) return fallback;
    return static_cast<uint32_t>(item->valueint);
}

static void scaled_dimensions(uint32_t width,
                              uint32_t height,
                              uint32_t max_edge,
                              uint32_t* out_width,
                              uint32_t* out_height) {
    *out_width = width;
    *out_height = height;
    if (max_edge == 0 || (width <= max_edge && height <= max_edge)) {
        return;
    }
    if (width >= height) {
        *out_width = max_edge;
        *out_height = (height * max_edge + width / 2) / width;
    } else {
        *out_height = max_edge;
        *out_width = (width * max_edge + height / 2) / height;
    }
    if (*out_width == 0) *out_width = 1;
    if (*out_height == 0) *out_height = 1;
}

std::string handle_frame_tool(const char* socket_path,
                              const char* tool_name,
                              const char* args_json,
                              const char* output_dir) {
    if (!is_frame_tool(tool_name)) {
        return "{\"ok\":false,\"error\":\"unknown frame tool\"}";
    }
    cJSON* args = cJSON_Parse(args_json ? args_json : "{}");
    if (!args) {
        return "{\"ok\":false,\"error\":\"invalid JSON\"}";
    }

    const char* path = socket_path && socket_path[0] ? socket_path : "/tmp/frame_service.sock";
    FrameServiceClient client(path);

    if (strcmp(tool_name, "frame_service_health") == 0) {
        cJSON_Delete(args);
        HealthResult health;
        FrameServiceStatus status = client.health(&health);
        if (status != FrameServiceStatus::OK) {
            return frame_tool_error(status);
        }
        return health_json(health);
    }

    if (strcmp(tool_name, "frame_service_restart") == 0) {
        cJSON_Delete(args);
        FrameServiceStatus status = client.restart();
        if (status != FrameServiceStatus::OK) {
            return frame_tool_error(status);
        }
        return "{\"ok\":true}";
    }

    std::string format = "png";
    uint32_t max_edge = json_u32(args, "max_edge", kDefaultScreenshotMaxEdge);
    cJSON* format_json = cJSON_GetObjectItem(args, "format");
    if (format_json && format_json->type == cJSON_String && format_json->valuestring) {
        format = format_json->valuestring;
    }
    if (format != "bmp" && format != "png") {
        cJSON_Delete(args);
        return "{\"ok\":false,\"error\":\"unsupported format\"}";
    }
    cJSON_Delete(args);

    FrameResult frame;
    FrameServiceStatus status = client.latest_frame(0, 0, &frame);
    if (status != FrameServiceStatus::OK) {
        return frame_tool_error(status);
    }

    uint32_t output_width = frame.metadata.width;
    uint32_t output_height = frame.metadata.height;
    scaled_dimensions(frame.metadata.width, frame.metadata.height, max_edge, &output_width, &output_height);

    std::vector<uint8_t> image;
    bool encoded = false;
    if (output_width != frame.metadata.width || output_height != frame.metadata.height) {
        std::vector<uint8_t> rgb;
        std::vector<uint8_t> scaled;
        encoded = convert_frame_to_rgb(frame.metadata, frame.data, &rgb) &&
                  scale_rgb_nearest(rgb, frame.metadata.width, frame.metadata.height,
                                    output_width, output_height, &scaled) &&
                  (format == "png"
                       ? encode_rgb_to_png(scaled, output_width, output_height, &image)
                       : encode_rgb_to_bmp(scaled, output_width, output_height, &image));
    } else {
        encoded = format == "png"
            ? encode_frame_to_png(frame.metadata, frame.data, &image)
            : encode_frame_to_bmp(frame.metadata, frame.data, &image);
    }
    if (!encoded) {
        return frame_tool_error(FrameServiceStatus::INTERNAL_ERROR);
    }

    const char* dir = output_dir && output_dir[0] ? output_dir : "/tmp";
    char file_path[512];
    snprintf(file_path, sizeof(file_path), "%s/aiden-frame-%llu.%s",
             dir, static_cast<unsigned long long>(frame.metadata.seq), format.c_str());
    if (!write_file(file_path, image)) {
        return frame_tool_error(FrameServiceStatus::INTERNAL_ERROR);
    }

    return std::string("{\"ok\":true,\"path\":\"") + json_escape(file_path) +
           "\",\"seq\":\"" + u64_text(frame.metadata.seq) +
           "\",\"format\":\"" + format +
           "\",\"width\":" + std::to_string(output_width) +
           ",\"height\":" + std::to_string(output_height) +
           ",\"source_width\":" + std::to_string(frame.metadata.width) +
           ",\"source_height\":" + std::to_string(frame.metadata.height) +
           ",\"pixel_format\":\"" + json_escape(frame.metadata.pixel_format) + "\"}";
}

}
