#include "doctest.h"
#include "wav_codec.h"
#include <cstdint>
#include <cstring>
#include <vector>

using aiden::pcm16_mono_16khz_to_wav;

namespace {

uint16_t le16_at(const std::vector<uint8_t>& bytes, size_t off) {
    return (uint16_t)bytes[off] | ((uint16_t)bytes[off + 1] << 8);
}

uint32_t le32_at(const std::vector<uint8_t>& bytes, size_t off) {
    return (uint32_t)bytes[off]
         | ((uint32_t)bytes[off + 1] << 8)
         | ((uint32_t)bytes[off + 2] << 16)
         | ((uint32_t)bytes[off + 3] << 24);
}

}

TEST_CASE("pcm16_mono_16khz_to_wav emits valid header for empty PCM") {
    std::vector<int16_t> pcm;
    std::vector<uint8_t> wav = pcm16_mono_16khz_to_wav(pcm);

    REQUIRE(wav.size() == 44);
    CHECK(std::memcmp(wav.data() + 0, "RIFF", 4) == 0);
    CHECK(le32_at(wav, 4) == 36);
    CHECK(std::memcmp(wav.data() + 8, "WAVE", 4) == 0);
    CHECK(std::memcmp(wav.data() + 12, "fmt ", 4) == 0);
    CHECK(le32_at(wav, 16) == 16);
    CHECK(le16_at(wav, 20) == 1);
    CHECK(le16_at(wav, 22) == 1);
    CHECK(le32_at(wav, 24) == 16000);
    CHECK(le32_at(wav, 28) == 32000);
    CHECK(le16_at(wav, 32) == 2);
    CHECK(le16_at(wav, 34) == 16);
    CHECK(std::memcmp(wav.data() + 36, "data", 4) == 0);
    CHECK(le32_at(wav, 40) == 0);
}

TEST_CASE("pcm16_mono_16khz_to_wav copies PCM payload bytes") {
    std::vector<int16_t> pcm = {0x1234, (int16_t)0xFEDC, 0x0001};
    std::vector<uint8_t> wav = pcm16_mono_16khz_to_wav(pcm);

    REQUIRE(wav.size() == 44 + pcm.size() * 2);
    CHECK(le32_at(wav, 4) == 36 + pcm.size() * 2);
    CHECK(le32_at(wav, 40) == pcm.size() * 2);

    CHECK(wav[44] == 0x34);
    CHECK(wav[45] == 0x12);
    CHECK(wav[46] == 0xDC);
    CHECK(wav[47] == 0xFE);
    CHECK(wav[48] == 0x01);
    CHECK(wav[49] == 0x00);
}

TEST_CASE("pcm16_mono_16khz_to_wav preserves exact sample count") {
    std::vector<int16_t> pcm(480, 42);
    std::vector<uint8_t> wav = pcm16_mono_16khz_to_wav(pcm);

    REQUIRE(wav.size() == 44 + 960);
    CHECK(le32_at(wav, 40) == 960);
}
