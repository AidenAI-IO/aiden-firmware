#include "tool_image_attachment.h"
#include "cJSON/cJSON.h"
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <vector>

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

static bool read_file_bytes(const char* path, std::vector<uint8_t>* out) {
    if (!path || !out) return false;
    FILE* fp = std::fopen(path, "rb");
    if (!fp) return false;
    if (std::fseek(fp, 0, SEEK_END) != 0) {
        std::fclose(fp);
        return false;
    }
    long size = std::ftell(fp);
    if (size < 0) {
        std::fclose(fp);
        return false;
    }
    if (std::fseek(fp, 0, SEEK_SET) != 0) {
        std::fclose(fp);
        return false;
    }
    out->resize(static_cast<size_t>(size));
    size_t read = out->empty() ? 0 : std::fread(out->data(), 1, out->size(), fp);
    std::fclose(fp);
    return read == out->size();
}

static const char* json_string(cJSON* object, const char* key) {
    cJSON* item = cJSON_GetObjectItem(object, key);
    if (!item || item->type != cJSON_String || !item->valuestring) return "";
    return item->valuestring;
}

static int json_int(cJSON* object, const char* key) {
    cJSON* item = cJSON_GetObjectItem(object, key);
    if (!item || item->type != cJSON_Number) return 0;
    return item->valueint;
}

bool build_image_attachment_from_tool_result(const char* tool_name,
                                             const char* result_json,
                                             ImageAttachment* out) {
    if (!tool_name || std::string(tool_name) != "capture_screenshot" || !result_json || !out) {
        return false;
    }

    cJSON* root = cJSON_Parse(result_json);
    if (!root) return false;
    cJSON* ok = cJSON_GetObjectItem(root, "ok");
    const char* format = json_string(root, "format");
    const char* path = json_string(root, "path");
    if (!ok || ok->type != cJSON_True || std::string(format) != "png" || path[0] == '\0') {
        cJSON_Delete(root);
        return false;
    }

    std::vector<uint8_t> bytes;
    if (!read_file_bytes(path, &bytes)) {
        cJSON_Delete(root);
        return false;
    }

    out->data_url = "data:image/png;base64," + base64_encode(bytes.data(), bytes.size());
    out->text = "Screenshot captured from frame_service (seq=";
    out->text += json_string(root, "seq");
    out->text += ", ";
    out->text += std::to_string(json_int(root, "width"));
    out->text += "x";
    out->text += std::to_string(json_int(root, "height"));
    out->text += ").";

    cJSON_Delete(root);
    return true;
}

}
