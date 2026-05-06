#pragma once
#include <cstdint>
#include <vector>

namespace aiden {

std::vector<uint8_t> pcm16_mono_16khz_to_wav(const std::vector<int16_t>& pcm);

}
