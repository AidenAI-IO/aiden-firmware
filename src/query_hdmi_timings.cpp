#include <errno.h>
#include <fcntl.h>
#include <linux/v4l2-subdev.h>
#include <linux/videodev2.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

static int xioctl(int fd, unsigned long request, void* arg) {
    int ret;
    do {
        ret = ioctl(fd, request, arg);
    } while (ret < 0 && errno == EINTR);
    return ret;
}

int main(int argc, char* argv[]) {
    const char* subdev_device = "/dev/v4l-subdev2";

    if (argc > 1) {
        subdev_device = argv[1];
    }

    printf("Querying HDMI timings from %s...\n\n", subdev_device);

    int fd = open(subdev_device, O_RDWR);
    if (fd < 0) {
        fprintf(stderr, "Failed to open %s: %s\n", subdev_device, strerror(errno));
        return 1;
    }

    struct v4l2_dv_timings timings;
    memset(&timings, 0, sizeof(timings));

    if (xioctl(fd, VIDIOC_SUBDEV_QUERY_DV_TIMINGS, &timings) < 0) {
        fprintf(stderr, "VIDIOC_SUBDEV_QUERY_DV_TIMINGS failed: %s\n", strerror(errno));
        close(fd);
        return 1;
    }

    if (timings.type != V4L2_DV_BT_656_1120) {
        fprintf(stderr, "Unexpected timing type: %u\n", timings.type);
        close(fd);
        return 1;
    }

    const struct v4l2_bt_timings* bt = &timings.bt;

    printf("Detected HDMI Resolution:\n");
    printf("  Width:  %u\n", bt->width);
    printf("  Height: %u\n", bt->height);
    printf("  Frame rate: %.2f Hz\n",
           (double)bt->pixelclock / (bt->width + bt->hfrontporch + bt->hsync + bt->hbackporch) /
           (bt->height + bt->vfrontporch + bt->vsync + bt->vbackporch));
    printf("  Interlaced: %s\n", (bt->interlaced ? "yes" : "no"));
    printf("\n");

    printf("Use these parameters with example_camera_capture:\n");
    printf("  example_camera_capture --width %u --height %u\n", bt->width, bt->height);
    printf("\n");

    printf("Or use auto-detect mode:\n");
    printf("  example_camera_capture --allow-resolution-mismatch\n");

    close(fd);
    return 0;
}
