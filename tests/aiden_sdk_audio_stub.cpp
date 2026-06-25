#include "aiden_sdk.h"

namespace aiden {

class AudioCaptureImpl {};
class AudioPlayerImpl {
public:
    int volume = 100;
    bool initialized = false;
};

AudioCapture::AudioCapture() : impl_(new AudioCaptureImpl()) {}
AudioCapture::~AudioCapture() {}

bool AudioCapture::init(const AudioConfig&) { return true; }
bool AudioCapture::start(AudioStreamCallback) { return true; }
void AudioCapture::stop() {}
bool AudioCapture::get_frame(AudioFrame&) { return false; }
void AudioCapture::release_frame() {}
bool AudioCapture::is_running() const { return false; }

AudioPlayer::AudioPlayer() : impl_(new AudioPlayerImpl()) {}
AudioPlayer::~AudioPlayer() {}

bool AudioPlayer::init(const AudioConfig&) {
    impl_->initialized = true;
    return true;
}

bool AudioPlayer::play(const void*, uint32_t) { return impl_->initialized; }
bool AudioPlayer::play(const AudioFrame& frame) { return play(frame.data, frame.length); }
void AudioPlayer::stop() {}
void AudioPlayer::pause() {}
void AudioPlayer::resume() {}

bool AudioPlayer::set_volume(int volume) {
    impl_->volume = volume;
    return true;
}

int AudioPlayer::get_volume() const { return impl_->volume; }
bool AudioPlayer::is_initialized() const { return impl_->initialized; }

}  // namespace aiden
