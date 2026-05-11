#include "stt/providers/openai_whisper_stt.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

namespace aiden {

OpenAIWhisperStt::OpenAIWhisperStt(const char* api_key, const char* model, const char* base_url)
    : api_key_(api_key ? api_key : ""),
      model_(model ? model : ""),
      base_url_(base_url ? base_url : "") {}

static std::string pick_transcription_url(const std::string& base_url) {
    if (base_url.empty()) {
        return "https://api.openai.com/v1/audio/transcriptions";
    }
    if (base_url.back() == '/') {
        return base_url + "audio/transcriptions";
    }
    return base_url + "/audio/transcriptions";
}

bool OpenAIWhisperStt::transcribe_wav(const uint8_t* wav_data, size_t wav_len, std::string& text) {
    text.clear();
    if (!wav_data || wav_len == 0) return false;

    char wav_path[] = "/tmp/aiden_stt_wav_XXXXXX";
    int wav_fd = mkstemp(wav_path);
    if (wav_fd < 0) return false;

    FILE* wav_fp = fdopen(wav_fd, "wb");
    if (!wav_fp) {
        close(wav_fd);
        unlink(wav_path);
        return false;
    }

    size_t written = fwrite(wav_data, 1, wav_len, wav_fp);
    fclose(wav_fp);
    if (written != wav_len) {
        unlink(wav_path);
        return false;
    }

    char resp_path[] = "/tmp/aiden_stt_resp_XXXXXX";
    int resp_fd = mkstemp(resp_path);
    if (resp_fd < 0) {
        unlink(wav_path);
        return false;
    }
    close(resp_fd);

    std::string url = pick_transcription_url(base_url_);

    char cmd[8192];
    snprintf(cmd, sizeof(cmd),
             "curl -s -X POST "
             "-H \"Authorization: Bearer %s\" "
             "-F \"model=%s\" "
             "-F \"file=@%s;type=audio/wav\" "
             "-o \"%s\" \"%s\"",
             api_key_.c_str(), model_.c_str(), wav_path, resp_path, url.c_str());

    int status = system(cmd);
    unlink(wav_path);
    if (status != 0) {
        unlink(resp_path);
        return false;
    }

    FILE* fp = fopen(resp_path, "rb");
    if (!fp) {
        unlink(resp_path);
        return false;
    }

    std::string body;
    char buf[4096];
    while (fgets(buf, sizeof(buf), fp)) {
        body += buf;
    }
    fclose(fp);
    unlink(resp_path);

    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return false;
    }

    cJSON* text_node = cJSON_GetObjectItem(root, "text");
    if (!text_node || text_node->type != cJSON_String || !text_node->valuestring) {
        cJSON_Delete(root);
        return false;
    }

    text = text_node->valuestring;
    cJSON_Delete(root);
    return true;
}

}
