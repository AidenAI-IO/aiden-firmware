#include <stdio.h>
#include <stdlib.h>
#include <fcntl.h>
#include <unistd.h>
#include <poll.h>
#include <string.h>
#include <errno.h>

typedef struct {
    const char* pin;
    const char* value_path;
    const char* direction_path;
    const char* edge_path;
} GpioPin;

static const GpioPin GPIO_PINS[] = {
    {"33", "/sys/class/gpio/gpio33/value", "/sys/class/gpio/gpio33/direction", "/sys/class/gpio/gpio33/edge"},
    {"32", "/sys/class/gpio/gpio32/value", "/sys/class/gpio/gpio32/direction", "/sys/class/gpio/gpio32/edge"},
};

static int gpio_pin_count() {
    return sizeof(GPIO_PINS) / sizeof(GPIO_PINS[0]);
}

static int write_gpio_setting(const char* path, const char* value, size_t value_len) {
    int fd = open(path, O_WRONLY);
    if (fd < 0) {
        return -errno;
    }

    ssize_t written = write(fd, value, value_len);
    if (written != (ssize_t)value_len) {
        int err = written < 0 ? errno : EIO;
        close(fd);
        return -err;
    }

    close(fd);
    return 0;
}

int init_gpio_pin(const GpioPin* gpio) {
    int status = write_gpio_setting("/sys/class/gpio/export", gpio->pin, strlen(gpio->pin));
    if (status != 0) {
        return status;
    }

    status = write_gpio_setting(gpio->direction_path, "in", 2);
    if (status != 0) {
        return status;
    }

    return write_gpio_setting(gpio->edge_path, "falling", 7);
}

int main() {
    int count = gpio_pin_count();
    struct pollfd pfd[sizeof(GPIO_PINS) / sizeof(GPIO_PINS[0])];
    char buf[64];

    for (int i = 0; i < count; i++) {
        int status = init_gpio_pin(&GPIO_PINS[i]);
        if (status != 0) {
            int err = status < 0 ? -status : status;
            fprintf(stderr, "Failed to initialize GPIO %s: %s (errno %d)\n",
                    GPIO_PINS[i].pin, strerror(err), err);
            for (int j = 0; j < i; j++) {
                close(pfd[j].fd);
            }
            return 1;
        }

        int fd = open(GPIO_PINS[i].value_path, O_RDONLY);
        if (fd < 0) {
            perror("Failed to open gpio value");
            for (int j = 0; j < i; j++) {
                close(pfd[j].fd);
            }
            return 1;
        }

        pfd[i].fd = fd;
        pfd[i].events = POLLPRI | POLLERR;
        pfd[i].revents = 0;
    }

    printf("Listening for falling edge on GPIO33 or GPIO32...\n");

    while (1) {
        for (int i = 0; i < count; i++) {
            lseek(pfd[i].fd, 0, SEEK_SET);
            read(pfd[i].fd, buf, sizeof(buf));
        }

        int ret = poll(pfd, count, -1);

        if (ret > 0) {
            for (int i = 0; i < count; i++) {
                if (pfd[i].revents & POLLPRI) {
                    printf("hello world\n");
                    usleep(150000);
                }
            }
        }
    }

    for (int i = 0; i < count; i++) {
        close(pfd[i].fd);
    }
    return 0;
}
