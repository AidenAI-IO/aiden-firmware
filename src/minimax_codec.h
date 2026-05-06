#pragma once
#include <cstddef>
#include <cstdint>
#include <vector>
#include <string>

namespace aiden {
namespace minimax {

std::vector<uint8_t> hex_decode(const char* hex);

class StreamParser {
public:
    std::vector<std::vector<uint8_t>> feed(const char* data, size_t len);
    void reset();

private:
    std::string buffer_;
    std::vector<uint8_t> previous_audio_;
};

}
}
