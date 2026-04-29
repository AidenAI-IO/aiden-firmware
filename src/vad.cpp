#include "vad.h"
#include <cstdlib>

namespace aiden {

AudioVAD::AudioVAD(int sample_rate, int energy_threshold,
                   int silence_ms, int min_speech_ms, bool always_buffer)
    : energy_threshold_(energy_threshold)
    , always_buffer_(always_buffer)
    , silence_count_(0)
    , speech_frames_(0)
    , speaking_(false)
{
    int frame_ms = 30;
    frame_samples_ = sample_rate * frame_ms / 1000;
    silence_limit_ = silence_ms / frame_ms;
    min_speech_frames_ = min_speech_ms / frame_ms;
}

static int compute_energy(const int16_t* samples, int count) {
    long sum = 0;
    for (int i = 0; i < count; i++)
        sum += abs(samples[i]);
    return (int)(sum / count);
}

const std::vector<int16_t>* AudioVAD::process(const int16_t* samples, int count) {
    int energy = compute_energy(samples, count);
    bool is_speech = energy > energy_threshold_;

    // In always_buffer mode, collect all frames regardless of speech detection
    if (always_buffer_) {
        speech_buf_.insert(speech_buf_.end(), samples, samples + count);
        if (is_speech) {
            speech_frames_++;
            speaking_ = true;
            silence_count_ = 0;
        } else if (speaking_) {
            silence_count_++;
            if (silence_count_ >= silence_limit_) {
                if (speech_frames_ >= min_speech_frames_) {
                    utterance_.swap(speech_buf_);
                    reset();
                    return &utterance_;
                }
                reset();
            }
        }
        return nullptr;
    }

    // Normal VAD mode: only buffer after speech detected
    if (is_speech) {
        speech_buf_.insert(speech_buf_.end(), samples, samples + count);
        silence_count_ = 0;
        speech_frames_++;
        speaking_ = true;
    } else if (speaking_) {
        speech_buf_.insert(speech_buf_.end(), samples, samples + count);
        silence_count_++;

        if (silence_count_ >= silence_limit_) {
            if (speech_frames_ >= min_speech_frames_) {
                utterance_.swap(speech_buf_);
                reset();
                return &utterance_;
            }
            reset();
        }
    }

    return nullptr;
}

const std::vector<int16_t>* AudioVAD::flush() {
    if (speech_buf_.empty())
        return nullptr;

    utterance_.swap(speech_buf_);
    reset();
    return &utterance_;
}

void AudioVAD::reset() {
    speech_buf_.clear();
    silence_count_ = 0;
    speech_frames_ = 0;
    speaking_ = false;
}

}
