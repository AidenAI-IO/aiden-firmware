#pragma once
#include "aiden_sdk.h"
#include <string>

namespace aiden {

class MinimaxTTS {
public:
    MinimaxTTS(const char* api_key, const char* voice_id,
               const char* emotion, float speed);

    // Stream TTS: decode MP3 chunks on-the-fly and play via AudioPlayer
    bool text_to_speech_stream(const char* text, aiden::AudioPlayer& player);

private:
    std::string api_key_;
    std::string voice_id_;
    std::string emotion_;
    float speed_;
};

}
