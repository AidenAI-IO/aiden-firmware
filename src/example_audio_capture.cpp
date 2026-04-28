#include "aiden_sdk.h"
#include <stdio.h>
#include <signal.h>
#include <unistd.h>

static bool quit = false;
static int frame_count = 0;

void signal_handler(int sig) {
    printf("Received signal %d, exiting...\n", sig);
    quit = true;
}

void on_audio_frame(const aiden::AudioFrame& frame) {
    frame_count++;
    printf("Received audio frame #%d: %u bytes, timestamp: %llu\n",
           frame_count, frame.length, frame.timestamp);
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);

    aiden::AudioConfig config;
    config.device_name = (argc > 1) ? argv[1] : nullptr;
    config.sample_rate = 16000;
    config.channels = 2;
    config.bit_width = 16;

    aiden::AudioCapture capture;

    printf("Initializing audio capture...\n");
    if (!capture.init(config)) {
        fprintf(stderr, "Failed to initialize audio capture\n");
        return 1;
    }

    printf("Starting audio capture...\n");
    if (!capture.start(on_audio_frame)) {
        fprintf(stderr, "Failed to start audio capture\n");
        return 1;
    }

    printf("Capturing audio. Press Ctrl+C to exit.\n");

    while (!quit) {
        sleep(1);
    }

    capture.stop();
    printf("Captured %d frames. Stopped.\n", frame_count);

    return 0;
}
