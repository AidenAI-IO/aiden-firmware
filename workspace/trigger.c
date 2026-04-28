#include <stdio.h>
#include <stdlib.h>
#include <fcntl.h>
#include <unistd.h>
#include <poll.h>
#include <string.h>

#define GPIO_PIN "33"
#define GPIO_VALUE_PATH "/sys/class/gpio/gpio33/value"
#define GPIO_EDGE_PATH "/sys/class/gpio/gpio33/edge"

void init_gpio() {
    int fd = open("/sys/class/gpio/export", O_WRONLY);
    if (fd != -1) {
        write(fd, GPIO_PIN, strlen(GPIO_PIN));
        close(fd);
    }

    fd = open("/sys/class/gpio/gpio33/direction", O_WRONLY);
    write(fd, "in", 2);
    close(fd);

    fd = open(GPIO_EDGE_PATH, O_WRONLY);
    write(fd, "falling", 7);
    close(fd);
}

int main() {
    int fd;
    struct pollfd pfd;
    char buf[64];

    init_gpio();

    fd = open(GPIO_VALUE_PATH, O_RDONLY);
    if (fd < 0) {
        perror("Failed to open gpio value");
        return 1;
    }

    pfd.fd = fd;
    pfd.events = POLLPRI | POLLERR;

    printf("Listening for falling edge on Pin 3 (GPIO33)...\n");

    while (1) {
        lseek(fd, 0, SEEK_SET);
        read(fd, buf, sizeof(buf));

        int ret = poll(&pfd, 1, -1);

        if (ret > 0 && (pfd.revents & POLLPRI)) {
            printf("hello world\n");
            usleep(150000);
        }
    }

    close(fd);
    return 0;
}
