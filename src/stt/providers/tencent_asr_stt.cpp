#include "stt/providers/tencent_asr_stt.h"
#include "openai_codec.h"
#include "cJSON/cJSON.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <stdint.h>

namespace aiden {

namespace {

static uint32_t rotr(uint32_t x, uint32_t n) { return (x >> n) | (x << (32 - n)); }

static void sha256(const uint8_t* data, size_t len, uint8_t out[32]) {
    static const uint32_t k[64] = {
        0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
        0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
        0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
        0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
        0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
        0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
        0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
        0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
    };

    uint32_t h[8] = {
        0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,
        0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19
    };

    uint64_t bit_len = (uint64_t)len * 8;
    size_t new_len = len + 1;
    while ((new_len % 64) != 56) new_len++;
    size_t total_len = new_len + 8;

    uint8_t* msg = (uint8_t*)malloc(total_len);
    if (!msg) {
        memset(out, 0, 32);
        return;
    }
    memcpy(msg, data, len);
    msg[len] = 0x80;
    memset(msg + len + 1, 0, new_len - len - 1);

    for (int i = 0; i < 8; i++) {
        msg[new_len + i] = (uint8_t)((bit_len >> (56 - 8 * i)) & 0xFF);
    }

    for (size_t chunk = 0; chunk < total_len; chunk += 64) {
        uint32_t w[64];
        for (int i = 0; i < 16; i++) {
            size_t j = chunk + i * 4;
            w[i] = ((uint32_t)msg[j] << 24) | ((uint32_t)msg[j + 1] << 16) |
                   ((uint32_t)msg[j + 2] << 8) | (uint32_t)msg[j + 3];
        }
        for (int i = 16; i < 64; i++) {
            uint32_t s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >> 3);
            uint32_t s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16] + s0 + w[i - 7] + s1;
        }

        uint32_t a = h[0], b = h[1], c = h[2], d = h[3];
        uint32_t e = h[4], f = h[5], g = h[6], hh = h[7];

        for (int i = 0; i < 64; i++) {
            uint32_t S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
            uint32_t ch = (e & f) ^ ((~e) & g);
            uint32_t temp1 = hh + S1 + ch + k[i] + w[i];
            uint32_t S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
            uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
            uint32_t temp2 = S0 + maj;

            hh = g;
            g = f;
            f = e;
            e = d + temp1;
            d = c;
            c = b;
            b = a;
            a = temp1 + temp2;
        }

        h[0] += a; h[1] += b; h[2] += c; h[3] += d;
        h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
    }

    free(msg);

    for (int i = 0; i < 8; i++) {
        out[i * 4] = (uint8_t)((h[i] >> 24) & 0xFF);
        out[i * 4 + 1] = (uint8_t)((h[i] >> 16) & 0xFF);
        out[i * 4 + 2] = (uint8_t)((h[i] >> 8) & 0xFF);
        out[i * 4 + 3] = (uint8_t)(h[i] & 0xFF);
    }
}

static std::string hex_encode(const uint8_t* data, size_t len) {
    static const char* kHex = "0123456789abcdef";
    std::string out;
    out.resize(len * 2);
    for (size_t i = 0; i < len; i++) {
        out[i * 2] = kHex[(data[i] >> 4) & 0x0F];
        out[i * 2 + 1] = kHex[data[i] & 0x0F];
    }
    return out;
}

static std::string sha256_hex(const std::string& input) {
    uint8_t out[32];
    sha256((const uint8_t*)input.data(), input.size(), out);
    return hex_encode(out, 32);
}

static std::string hmac_sha256_raw(const std::string& key, const std::string& msg) {
    const size_t block_size = 64;
    uint8_t k0[block_size];
    memset(k0, 0, sizeof(k0));

    if (key.size() > block_size) {
        uint8_t key_hash[32];
        sha256((const uint8_t*)key.data(), key.size(), key_hash);
        memcpy(k0, key_hash, 32);
    } else {
        memcpy(k0, key.data(), key.size());
    }

    uint8_t ipad[block_size], opad[block_size];
    for (size_t i = 0; i < block_size; i++) {
        ipad[i] = k0[i] ^ 0x36;
        opad[i] = k0[i] ^ 0x5c;
    }

    std::string inner;
    inner.reserve(block_size + msg.size());
    inner.append((const char*)ipad, block_size);
    inner.append(msg);

    uint8_t inner_hash[32];
    sha256((const uint8_t*)inner.data(), inner.size(), inner_hash);

    std::string outer;
    outer.reserve(block_size + 32);
    outer.append((const char*)opad, block_size);
    outer.append((const char*)inner_hash, 32);

    uint8_t final_hash[32];
    sha256((const uint8_t*)outer.data(), outer.size(), final_hash);
    return std::string((const char*)final_hash, 32);
}

static std::string hmac_sha256_hex(const std::string& key, const std::string& msg) {
    std::string raw = hmac_sha256_raw(key, msg);
    return hex_encode((const uint8_t*)raw.data(), raw.size());
}

static std::string shell_escape_double(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 8);
    for (size_t i = 0; i < s.size(); i++) {
        char c = s[i];
        if (c == '\\' || c == '"' || c == '$' || c == '`') out.push_back('\\');
        out.push_back(c);
    }
    return out;
}

} // namespace

TencentAsrStt::TencentAsrStt(const char* secret_id,
                             const char* secret_key,
                             const char* region,
                             const char* engine_model_type)
    : secret_id_(secret_id ? secret_id : ""),
      secret_key_(secret_key ? secret_key : ""),
      region_(region ? region : "ap-guangzhou"),
      engine_model_type_(engine_model_type ? engine_model_type : "16k_zh") {}

bool TencentAsrStt::transcribe_wav(const uint8_t* wav_data, size_t wav_len, std::string& text) {
    text.clear();
    if (!wav_data || wav_len == 0) return false;

    std::string audio_b64 = openai::base64_encode(wav_data, wav_len);

    cJSON* req = cJSON_CreateObject();
    cJSON_AddStringToObject(req, "EngSerViceType", engine_model_type_.c_str());
    cJSON_AddNumberToObject(req, "SourceType", 1);
    cJSON_AddStringToObject(req, "VoiceFormat", "wav");
    cJSON_AddStringToObject(req, "Data", audio_b64.c_str());
    cJSON_AddNumberToObject(req, "DataLen", (double)wav_len);

    char* req_str = cJSON_PrintUnformatted(req);
    cJSON_Delete(req);
    if (!req_str) return false;
    std::string payload(req_str);
    free(req_str);

    const std::string host = "asr.tencentcloudapi.com";
    const std::string service = "asr";
    const std::string algorithm = "TC3-HMAC-SHA256";

    time_t now = time(NULL);
    if (now < 1700000000) {
        fprintf(stderr, "[tencent_asr] system time is too old (%ld), signature may fail\n", (long)now);
    }
    char date_buf[16];
    struct tm tmbuf;
    gmtime_r(&now, &tmbuf);
    strftime(date_buf, sizeof(date_buf), "%Y-%m-%d", &tmbuf);
    std::string date = date_buf;

    char ts_buf[32];
    snprintf(ts_buf, sizeof(ts_buf), "%ld", (long)now);
    std::string timestamp = ts_buf;

    std::string canonical_headers =
        "content-type:application/json; charset=utf-8\n"
        "host:" + host + "\n";
    std::string signed_headers = "content-type;host";
    std::string hashed_payload = sha256_hex(payload);

    std::string canonical_request =
        "POST\n"
        "/\n"
        "\n" +
        canonical_headers +
        "\n" +
        signed_headers + "\n" +
        hashed_payload;

    std::string credential_scope = date + "/" + service + "/tc3_request";
    std::string string_to_sign =
        algorithm + "\n" +
        timestamp + "\n" +
        credential_scope + "\n" +
        sha256_hex(canonical_request);

    std::string secret_date = hmac_sha256_raw("TC3" + secret_key_, date);
    std::string secret_service = hmac_sha256_raw(secret_date, service);
    std::string secret_signing = hmac_sha256_raw(secret_service, "tc3_request");
    std::string signature = hmac_sha256_hex(secret_signing, string_to_sign);

    std::string authorization =
        algorithm + " Credential=" + secret_id_ + "/" + credential_scope +
        ", SignedHeaders=" + signed_headers +
        ", Signature=" + signature;

    char body_path[] = "/tmp/tencent_stt_req_XXXXXX";
    int body_fd = mkstemp(body_path);
    if (body_fd < 0) return false;
    FILE* body_fp = fdopen(body_fd, "wb");
    if (!body_fp) {
        close(body_fd);
        unlink(body_path);
        return false;
    }
    fwrite(payload.data(), 1, payload.size(), body_fp);
    fclose(body_fp);

    char resp_path[] = "/tmp/tencent_stt_resp_XXXXXX";
    int resp_fd = mkstemp(resp_path);
    if (resp_fd < 0) {
        unlink(body_path);
        return false;
    }
    close(resp_fd);

    std::string auth_esc = shell_escape_double(authorization);
    std::string region_esc = shell_escape_double(region_);

    char cmd[8192];
    snprintf(cmd, sizeof(cmd),
             "curl -s -X POST "
             "-H \"Authorization: %s\" "
             "-H \"Content-Type: application/json; charset=utf-8\" "
             "-H \"Host: asr.tencentcloudapi.com\" "
             "-H \"X-TC-Action: SentenceRecognition\" "
             "-H \"X-TC-Version: 2019-06-14\" "
             "-H \"X-TC-Timestamp: %s\" "
             "-H \"X-TC-Region: %s\" "
             "-d @\"%s\" "
             "-o \"%s\" \"https://asr.tencentcloudapi.com\"",
             auth_esc.c_str(), timestamp.c_str(), region_esc.c_str(), body_path, resp_path);

    int status = system(cmd);
    unlink(body_path);
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
    while (fgets(buf, sizeof(buf), fp)) body += buf;
    fclose(fp);
    unlink(resp_path);

    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) return false;

    cJSON* resp = cJSON_GetObjectItem(root, "Response");
    if (!resp || resp->type != cJSON_Object) {
        cJSON_Delete(root);
        return false;
    }

    cJSON* err = cJSON_GetObjectItem(resp, "Error");
    if (err && err->type == cJSON_Object) {
        cJSON* msg = cJSON_GetObjectItem(err, "Message");
        if (msg && msg->type == cJSON_String && msg->valuestring) {
            fprintf(stderr, "[tencent_asr] API error: %s\n", msg->valuestring);
        }
        cJSON_Delete(root);
        return false;
    }

    cJSON* result = cJSON_GetObjectItem(resp, "Result");
    if (!result || result->type != cJSON_String || !result->valuestring) {
        fprintf(stderr, "[tencent_asr] invalid response (missing Result): %s\n", body.c_str());
        cJSON_Delete(root);
        return false;
    }

    text = result->valuestring;
    if (text.empty()) {
        fprintf(stderr, "[tencent_asr] empty transcript response: %s\n", body.c_str());
    }

    cJSON_Delete(root);
    return true;
}

}
