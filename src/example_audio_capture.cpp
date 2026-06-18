#include "aiden_sdk.h"
#include <stdio.h>
#include <signal.h>
#include <stdint.h>
#include <unistd.h>

static bool quit = false;
static int frame_count = 0;
static FILE* audio_fp = nullptr;

void signal_handler(int sig) {
    printf("Received signal %d, exiting...\n", sig);
    quit = true;
}

void on_audio_frame(const aiden::AudioFrame& frame) {
    frame_count++;
    if (audio_fp) {
        const int channels = frame.channels > 0 ? frame.channels : 2;
        const int bit_width = frame.bit_width > 0 ? frame.bit_width : 16;
        if (frame.data && bit_width == 16) {
            const int16_t* samples = static_cast<const int16_t*>(frame.data);
            const uint32_t total_samples = frame.length / sizeof(int16_t);
            if (channels <= 1) {
                fwrite(samples, sizeof(int16_t), total_samples, audio_fp);
            } else {
                const uint32_t frame_count_per_channel = total_samples / channels;
                for (uint32_t i = 0; i < frame_count_per_channel; i++) {
                    fwrite(&samples[i * channels], sizeof(int16_t), 1, audio_fp);
                }
            }
        }
        fflush(audio_fp);
    }
    printf("Received audio frame #%d: %u bytes, sr=%d ch=%d bits=%d vqe=%d\n",
           frame_count, frame.length, frame.sample_rate, frame.channels,
           frame.bit_width, frame.vqe_processed ? 1 : 0);
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);

    aiden::AudioConfig config;
    config.device_name = (argc > 1) ? argv[1] : config.device_name;
    config.sample_rate = 16000;
    config.channels = 1;
    config.bit_width = 16;

    audio_fp = fopen("audio_capture_debug.pcm", "wb");
    if (!audio_fp) {
        fprintf(stderr, "Failed to open audio_capture_debug.pcm\n");
        return 1;
    }

    aiden::AudioCapture capture;

    printf("Initializing audio capture...\n");
    if (!capture.init(config)) {
        fprintf(stderr, "Failed to initialize audio capture\n");
        fclose(audio_fp);
        return 1;
    }

    printf("Starting audio capture...\n");
    if (!capture.start(on_audio_frame)) {
        fprintf(stderr, "Failed to start audio capture\n");
        fclose(audio_fp);
        return 1;
    }

    printf("Capturing audio (16kHz/16bit/mono). Press Ctrl+C to exit.\n");
    printf("Saving raw PCM to audio_capture_debug.pcm\n");

    while (!quit) {
        sleep(1);
    }

    capture.stop();
    fclose(audio_fp);
    printf("Captured %d frames. Saved to audio_capture_debug.pcm. Stopped.\n", frame_count);

    return 0;
}
