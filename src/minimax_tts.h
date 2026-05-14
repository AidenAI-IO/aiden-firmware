#pragma once
#include "provider_interfaces.h"
#include "audio_service_client.h"
#include <string>

namespace aiden {

class MinimaxTTS : public TtsClient {
public:
    MinimaxTTS(const char* api_key, const char* voice_id,
               const char* emotion, float speed);

    bool text_to_speech_stream(const char* text,
                               aiden::AudioServiceClient& audio) override;

private:
    std::string api_key_;
    std::string voice_id_;
    std::string emotion_;
    float speed_;
};

}
