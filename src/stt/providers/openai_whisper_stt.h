#pragma once
#include "provider_interfaces.h"
#include <string>

namespace aiden {

class OpenAIWhisperStt : public SttClient {
public:
    OpenAIWhisperStt(const char* api_key, const char* model, const char* base_url = "");

    bool transcribe_wav(const uint8_t* wav_data, size_t wav_len, std::string& text) override;

private:
    std::string api_key_;
    std::string model_;
    std::string base_url_;
};

}
