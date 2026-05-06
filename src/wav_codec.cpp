#include "wav_codec.h"
#include <string.h>

namespace aiden {

static void write_le16(uint8_t*& p, uint16_t value) {
    *p++ = (uint8_t)(value & 0xFF);
    *p++ = (uint8_t)((value >> 8) & 0xFF);
}

static void write_le32(uint8_t*& p, uint32_t value) {
    *p++ = (uint8_t)(value & 0xFF);
    *p++ = (uint8_t)((value >> 8) & 0xFF);
    *p++ = (uint8_t)((value >> 16) & 0xFF);
    *p++ = (uint8_t)((value >> 24) & 0xFF);
}

std::vector<uint8_t> pcm16_mono_16khz_to_wav(const std::vector<int16_t>& pcm) {
    uint32_t data_size = (uint32_t)(pcm.size() * sizeof(int16_t));
    uint32_t file_size = 36 + data_size;
    std::vector<uint8_t> wav(44 + data_size);
    uint8_t* p = wav.data();

    memcpy(p, "RIFF", 4); p += 4;
    write_le32(p, file_size);
    memcpy(p, "WAVE", 4); p += 4;
    memcpy(p, "fmt ", 4); p += 4;
    write_le32(p, 16);
    write_le16(p, 1);
    write_le16(p, 1);
    write_le32(p, 16000);
    write_le32(p, 32000);
    write_le16(p, 2);
    write_le16(p, 16);
    memcpy(p, "data", 4); p += 4;
    write_le32(p, data_size);
    if (data_size > 0)
        memcpy(p, pcm.data(), data_size);

    return wav;
}

}
