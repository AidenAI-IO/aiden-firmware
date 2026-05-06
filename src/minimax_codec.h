#pragma once
#include <cstddef>
#include <cstdint>
#include <vector>
#include <string>

namespace aiden {
namespace minimax {

std::vector<uint8_t> hex_decode(const char* hex);

struct StreamChunk {
    std::vector<uint8_t> audio;
    bool reset_decoder = false;
};

class StreamParser {
public:
    std::vector<StreamChunk> feed(const char* data, size_t len);
    void reset();

private:
    std::string buffer_;
    std::vector<uint8_t> previous_audio_;
};

}
}
