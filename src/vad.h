#pragma once
#include <vector>
#include <cstdint>

namespace aiden {

class AudioVAD {
public:
    AudioVAD(int sample_rate, int energy_threshold,
             int silence_ms, int min_speech_ms, bool always_buffer = false);

    // Feed a PCM frame. Returns non-null when a complete utterance is ready.
    const std::vector<int16_t>* process(const int16_t* samples, int count);

    // Flush accumulated audio (for manual stop). Returns null if nothing collected.
    const std::vector<int16_t>* flush();

    void reset();

private:
    int frame_samples_;
    int energy_threshold_;
    int silence_limit_;
    int min_speech_frames_;
    bool always_buffer_;

    std::vector<int16_t> speech_buf_;
    int silence_count_;
    int speech_frames_;
    bool speaking_;

    std::vector<int16_t> utterance_;
};

}
