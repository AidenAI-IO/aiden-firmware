#include "aiden_sdk.h"
#include <stdio.h>
#include <signal.h>
#include <stdint.h>
#include <unistd.h>

static bool quit = false;

void signal_handler(int sig) { quit = true; }

void on_audio_frame(const aiden::AudioFrame& frame) {
    const int channels = frame.channels > 0 ? frame.channels : 2;
    const int bit_width = frame.bit_width > 0 ? frame.bit_width : 16;
    if (!frame.data || bit_width != 16) {
        return;
    }

    const int16_t* samples = static_cast<const int16_t*>(frame.data);
    const uint32_t total_samples = frame.length / sizeof(int16_t);
    if (channels <= 1) {
        fwrite(samples, sizeof(int16_t), total_samples, stdout);
    } else {
        const uint32_t frame_count_per_channel = total_samples / channels;
        for (uint32_t i = 0; i < frame_count_per_channel; i++) {
            fwrite(&samples[i * channels], sizeof(int16_t), 1, stdout);
        }
    }
    fflush(stdout);
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);
    signal(SIGPIPE, signal_handler);

    aiden::AudioConfig config;
    config.device_name = (argc > 1) ? argv[1] : config.device_name;
    config.sample_rate = 16000;
    config.channels = 1;
    config.bit_width = 16;

    aiden::AudioCapture capture;
    if (!capture.init(config) || !capture.start(on_audio_frame))
        return 1;

    while (!quit) sleep(1);

    capture.stop();
    return 0;
}
