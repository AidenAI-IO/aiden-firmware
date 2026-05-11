#pragma once
#include "provider_interfaces.h"
#include <string>

namespace aiden {

class TencentAsrStt : public SttClient {
public:
    TencentAsrStt(const char* secret_id,
                  const char* secret_key,
                  const char* region,
                  const char* engine_model_type);

    bool transcribe_wav(const uint8_t* wav_data, size_t wav_len, std::string& text) override;

private:
    std::string secret_id_;
    std::string secret_key_;
    std::string region_;
    std::string engine_model_type_;
};

}
