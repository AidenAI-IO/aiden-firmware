#include "http_client.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

namespace aiden {

bool HttpClient::is_available() {
    return access("/usr/bin/curl", X_OK) == 0;
}

static bool write_temp(const char* data, size_t len, char* path_out) {
    strcpy(path_out, "/tmp/agent_req_XXXXXX");
    int fd = mkstemp(path_out);
    if (fd < 0) return false;
    write(fd, data, len);
    close(fd);
    return true;
}

bool HttpClient::post_json(const char* url, const char* auth_header,
                           const char* json_body, std::string& response) {
    char body_file[64];
    if (!write_temp(json_body, strlen(json_body), body_file))
        return false;

    fprintf(stderr, "[http] POST %s\n", url);
    fprintf(stderr, "[http] Request body length: %zu bytes\n", strlen(json_body));

    char cmd[4096];
    snprintf(cmd, sizeof(cmd),
        "curl -s -X POST "
        "-H 'Content-Type: application/json' "
        "-H 'Authorization: Bearer %s' "
        "-d @%s '%s'",
        auth_header, body_file, url);

    FILE* fp = popen(cmd, "r");
    if (!fp) {
        fprintf(stderr, "[http] Failed to execute curl\n");
        unlink(body_file);
        return false;
    }

    char buf[4096];
    response.clear();
    while (fgets(buf, sizeof(buf), fp))
        response += buf;

    int status = pclose(fp);
    unlink(body_file);

    fprintf(stderr, "[http] Response length: %zu bytes\n", response.size());
    if (status != 0) {
        fprintf(stderr, "[http] curl exited with status %d\n", status);
        fprintf(stderr, "[http] Response: %s\n", response.c_str());
        return false;
    }

    return true;
}

bool HttpClient::post_binary(const char* url, const char* auth_header,
                             const char* json_body, std::vector<uint8_t>& response) {
    char body_file[64];
    if (!write_temp(json_body, strlen(json_body), body_file))
        return false;

    fprintf(stderr, "[http] POST %s (binary response expected)\n", url);
    fprintf(stderr, "[http] Request body length: %zu bytes\n", strlen(json_body));

    char out_file[] = "/tmp/agent_out_XXXXXX";
    int fd = mkstemp(out_file);
    if (fd < 0) { unlink(body_file); return false; }
    close(fd);

    char cmd[4096];
    snprintf(cmd, sizeof(cmd),
        "curl -s -X POST "
        "-H 'Content-Type: application/json' "
        "-H 'Authorization: Bearer %s' "
        "-d @%s -o '%s' '%s'",
        auth_header, body_file, out_file, url);

    int status = system(cmd);
    unlink(body_file);

    if (status != 0) {
        fprintf(stderr, "[http] curl exited with status %d\n", status);
        unlink(out_file);
        return false;
    }

    FILE* fp = fopen(out_file, "rb");
    if (!fp) {
        fprintf(stderr, "[http] Failed to open output file\n");
        unlink(out_file);
        return false;
    }

    fseek(fp, 0, SEEK_END);
    long size = ftell(fp);
    fseek(fp, 0, SEEK_SET);

    response.resize(size);
    fread(response.data(), 1, size, fp);
    fclose(fp);
    unlink(out_file);

    fprintf(stderr, "[http] Binary response length: %ld bytes\n", size);
    return true;
}

bool HttpClient::post_stream(const char* url, const char* auth_header,
                             const char* json_body, StreamChunkCallback callback,
                             void* user_data) {
    char body_file[64];
    if (!write_temp(json_body, strlen(json_body), body_file))
        return false;

    fprintf(stderr, "[http] POST %s (streaming)\n", url);
    fprintf(stderr, "[http] Request body length: %zu bytes\n", strlen(json_body));

    char cmd[4096];
    snprintf(cmd, sizeof(cmd),
        "curl -s -N -X POST "
        "-H 'Content-Type: application/json' "
        "-H 'Authorization: Bearer %s' "
        "-d @%s '%s'",
        auth_header, body_file, url);

    FILE* fp = popen(cmd, "r");
    if (!fp) {
        fprintf(stderr, "[http] Failed to execute curl\n");
        unlink(body_file);
        return false;
    }

    char buf[4096];
    size_t total = 0;
    while (fgets(buf, sizeof(buf), fp)) {
        size_t len = strlen(buf);
        total += len;
        callback(buf, len, user_data);
    }

    int status = pclose(fp);
    unlink(body_file);

    fprintf(stderr, "[http] Stream complete, %zu bytes received\n", total);
    if (status != 0) {
        fprintf(stderr, "[http] curl exited with status %d\n", status);
        return false;
    }

    return true;
}

}
