#include "aiden_sdk.h"
#include <stdio.h>
#include <signal.h>
#include <unistd.h>

static bool quit = false;

void signal_handler(int sig) {
    printf("Received signal %d, exiting...\n", sig);
    quit = true;
}

void on_wakeup() {
    printf("Wakeup event detected!\n");
}

int main() {
    signal(SIGINT, signal_handler);

    aiden::WakeupListener listener;

    printf("Starting wakeup listener on GPIO 33...\n");
    if (!listener.start(33, on_wakeup)) {
        fprintf(stderr, "Failed to start wakeup listener\n");
        return 1;
    }

    printf("Listening for wakeup events. Press Ctrl+C to exit.\n");

    while (!quit) {
        sleep(1);
    }

    listener.stop();
    printf("Stopped.\n");

    return 0;
}
