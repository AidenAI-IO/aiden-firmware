#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <linux/videodev2.h>

static bool quit = false;
static int frame_count = 0;

struct buffer {
    void* start;
    size_t length;
};

void signal_handler(int sig) {
    printf("Received signal %d, exiting...\n", sig);
    quit = true;
}

int xioctl(int fd, unsigned long request, void* arg) {
    int r;
    do {
        r = ioctl(fd, request, arg);
    } while (r == -1 && errno == EINTR);
    return r;
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);

    const char* device = (argc > 1) ? argv[1] : "/dev/video0";
    int width = (argc > 2) ? atoi(argv[2]) : 640;
    int height = (argc > 3) ? atoi(argv[3]) : 480;

    printf("Opening device: %s\n", device);
    int fd = open(device, O_RDWR);
    if (fd < 0) {
        fprintf(stderr, "Failed to open %s: %s\n", device, strerror(errno));
        return 1;
    }

    // Query capabilities
    struct v4l2_capability cap;
    if (xioctl(fd, VIDIOC_QUERYCAP, &cap) < 0) {
        fprintf(stderr, "VIDIOC_QUERYCAP failed: %s\n", strerror(errno));
        close(fd);
        return 1;
    }

    printf("Driver: %s\n", cap.driver);
    printf("Card: %s\n", cap.card);
    printf("Capabilities: 0x%08x\n", cap.capabilities);

    // Check if it's a video capture device
    bool is_mplane = cap.capabilities & V4L2_CAP_VIDEO_CAPTURE_MPLANE;
    bool is_single = cap.capabilities & V4L2_CAP_VIDEO_CAPTURE;

    if (!is_mplane && !is_single) {
        fprintf(stderr, "Device is not a video capture device\n");
        close(fd);
        return 1;
    }

    printf("Using %s mode\n", is_mplane ? "multiplanar" : "single-planar");

    // For TC358743: Query and set DV timings
    struct v4l2_dv_timings timings;
    memset(&timings, 0, sizeof(timings));

    if (xioctl(fd, VIDIOC_QUERY_DV_TIMINGS, &timings) == 0) {
        printf("Detected DV timings: %ux%u@%u.%02ufps\n",
               timings.bt.width, timings.bt.height,
               timings.bt.pixelclock / (timings.bt.width + timings.bt.hfrontporch +
                                        timings.bt.hsync + timings.bt.hbackporch) /
                                       (timings.bt.height + timings.bt.vfrontporch +
                                        timings.bt.vsync + timings.bt.vbackporch) / 1000000,
               (timings.bt.pixelclock / (timings.bt.width + timings.bt.hfrontporch +
                                         timings.bt.hsync + timings.bt.hbackporch) /
                                        (timings.bt.height + timings.bt.vfrontporch +
                                         timings.bt.vsync + timings.bt.vbackporch) % 1000000) / 10000);

        if (xioctl(fd, VIDIOC_S_DV_TIMINGS, &timings) < 0) {
            fprintf(stderr, "Warning: VIDIOC_S_DV_TIMINGS failed: %s\n", strerror(errno));
        } else {
            printf("DV timings set successfully\n");
            // Update width/height from detected timings
            width = timings.bt.width;
            height = timings.bt.height;
        }
    } else {
        printf("No DV timings detected (not a TC358743?), using %dx%d\n", width, height);
    }

    // Set format
    struct v4l2_format fmt;
    memset(&fmt, 0, sizeof(fmt));

    if (is_mplane) {
        fmt.type = V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE;
        fmt.fmt.pix_mp.width = width;
        fmt.fmt.pix_mp.height = height;
        fmt.fmt.pix_mp.pixelformat = V4L2_PIX_FMT_NV12;
        fmt.fmt.pix_mp.field = V4L2_FIELD_NONE;
    } else {
        fmt.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
        fmt.fmt.pix.width = width;
        fmt.fmt.pix.height = height;
        fmt.fmt.pix.pixelformat = V4L2_PIX_FMT_NV12;
        fmt.fmt.pix.field = V4L2_FIELD_NONE;
    }

    if (xioctl(fd, VIDIOC_S_FMT, &fmt) < 0) {
        fprintf(stderr, "VIDIOC_S_FMT failed: %s\n", strerror(errno));
        close(fd);
        return 1;
    }

    if (is_mplane) {
        printf("Format set: %ux%u, pixfmt=0x%08x (%c%c%c%c)\n",
               fmt.fmt.pix_mp.width, fmt.fmt.pix_mp.height,
               fmt.fmt.pix_mp.pixelformat,
               (fmt.fmt.pix_mp.pixelformat >> 0) & 0xff,
               (fmt.fmt.pix_mp.pixelformat >> 8) & 0xff,
               (fmt.fmt.pix_mp.pixelformat >> 16) & 0xff,
               (fmt.fmt.pix_mp.pixelformat >> 24) & 0xff);
    } else {
        printf("Format set: %ux%u, pixfmt=0x%08x (%c%c%c%c)\n",
               fmt.fmt.pix.width, fmt.fmt.pix.height,
               fmt.fmt.pix.pixelformat,
               (fmt.fmt.pix.pixelformat >> 0) & 0xff,
               (fmt.fmt.pix.pixelformat >> 8) & 0xff,
               (fmt.fmt.pix.pixelformat >> 16) & 0xff,
               (fmt.fmt.pix.pixelformat >> 24) & 0xff);
    }

    // Request buffers
    struct v4l2_requestbuffers req;
    memset(&req, 0, sizeof(req));
    req.count = 4;
    req.type = is_mplane ? V4L2_BUF_TYPE_VIDEO_CAPTURE_MPLANE : V4L2_BUF_TYPE_VIDEO_CAPTURE;
    req.memory = V4L2_MEMORY_MMAP;

    if (xioctl(fd, VIDIOC_REQBUFS, &req) < 0) {
        fprintf(stderr, "VIDIOC_REQBUFS failed: %s\n", strerror(errno));
        close(fd);
        return 1;
    }

    printf("Allocated %u buffers\n", req.count);

    struct buffer* buffers = (struct buffer*)calloc(req.count, sizeof(*buffers));
    if (!buffers) {
        fprintf(stderr, "Out of memory\n");
        close(fd);
        return 1;
    }

    // Map buffers
    for (unsigned int i = 0; i < req.count; i++) {
        struct v4l2_buffer buf;
        struct v4l2_plane planes[VIDEO_MAX_PLANES];
        memset(&buf, 0, sizeof(buf));
        memset(planes, 0, sizeof(planes));

        buf.type = req.type;
        buf.memory = V4L2_MEMORY_MMAP;
        buf.index = i;

        if (is_mplane) {
            buf.m.planes = planes;
            buf.length = VIDEO_MAX_PLANES;
        }

        if (xioctl(fd, VIDIOC_QUERYBUF, &buf) < 0) {
            fprintf(stderr, "VIDIOC_QUERYBUF failed: %s\n", strerror(errno));
            free(buffers);
            close(fd);
            return 1;
        }

        if (is_mplane) {
            buffers[i].length = buf.m.planes[0].length;
            buffers[i].start = mmap(NULL, buf.m.planes[0].length,
                                   PROT_READ | PROT_WRITE, MAP_SHARED,
                                   fd, buf.m.planes[0].m.mem_offset);
        } else {
            buffers[i].length = buf.length;
            buffers[i].start = mmap(NULL, buf.length,
                                   PROT_READ | PROT_WRITE, MAP_SHARED,
                                   fd, buf.m.offset);
        }

        if (buffers[i].start == MAP_FAILED) {
            fprintf(stderr, "mmap failed: %s\n", strerror(errno));
            free(buffers);
            close(fd);
            return 1;
        }

        printf("Buffer %u: %p, length=%zu\n", i, buffers[i].start, buffers[i].length);
    }

    // Queue buffers
    for (unsigned int i = 0; i < req.count; i++) {
        struct v4l2_buffer buf;
        struct v4l2_plane planes[VIDEO_MAX_PLANES];
        memset(&buf, 0, sizeof(buf));
        memset(planes, 0, sizeof(planes));

        buf.type = req.type;
        buf.memory = V4L2_MEMORY_MMAP;
        buf.index = i;

        if (is_mplane) {
            buf.m.planes = planes;
            buf.length = VIDEO_MAX_PLANES;
        }

        if (xioctl(fd, VIDIOC_QBUF, &buf) < 0) {
            fprintf(stderr, "VIDIOC_QBUF failed: %s\n", strerror(errno));
            for (unsigned int j = 0; j < req.count; j++) {
                munmap(buffers[j].start, buffers[j].length);
            }
            free(buffers);
            close(fd);
            return 1;
        }
    }

    // Start streaming
    enum v4l2_buf_type type = (enum v4l2_buf_type)req.type;
    if (xioctl(fd, VIDIOC_STREAMON, &type) < 0) {
        fprintf(stderr, "VIDIOC_STREAMON failed: %s\n", strerror(errno));
        for (unsigned int i = 0; i < req.count; i++) {
            munmap(buffers[i].start, buffers[i].length);
        }
        free(buffers);
        close(fd);
        return 1;
    }

    printf("Streaming started. Press Ctrl+C to exit.\n");

    // Capture loop
    while (!quit) {
        fd_set fds;
        struct timeval tv;

        FD_ZERO(&fds);
        FD_SET(fd, &fds);

        tv.tv_sec = 2;
        tv.tv_usec = 0;

        int r = select(fd + 1, &fds, NULL, NULL, &tv);
        if (r == -1) {
            if (errno == EINTR) continue;
            fprintf(stderr, "select failed: %s\n", strerror(errno));
            break;
        }

        if (r == 0) {
            fprintf(stderr, "select timeout\n");
            continue;
        }

        struct v4l2_buffer buf;
        struct v4l2_plane planes[VIDEO_MAX_PLANES];
        memset(&buf, 0, sizeof(buf));
        memset(planes, 0, sizeof(planes));

        buf.type = req.type;
        buf.memory = V4L2_MEMORY_MMAP;

        if (is_mplane) {
            buf.m.planes = planes;
            buf.length = VIDEO_MAX_PLANES;
        }

        if (xioctl(fd, VIDIOC_DQBUF, &buf) < 0) {
            if (errno == EAGAIN) continue;
            fprintf(stderr, "VIDIOC_DQBUF failed: %s\n", strerror(errno));
            break;
        }

        frame_count++;
        size_t bytes_used = is_mplane ? buf.m.planes[0].bytesused : buf.bytesused;
        printf("Frame #%d: index=%u, bytes=%zu, seq=%u, timestamp=%ld.%06ld\n",
               frame_count, buf.index, bytes_used, buf.sequence,
               buf.timestamp.tv_sec, buf.timestamp.tv_usec);

        if (xioctl(fd, VIDIOC_QBUF, &buf) < 0) {
            fprintf(stderr, "VIDIOC_QBUF failed: %s\n", strerror(errno));
            break;
        }
    }

    // Stop streaming
    if (xioctl(fd, VIDIOC_STREAMOFF, &type) < 0) {
        fprintf(stderr, "VIDIOC_STREAMOFF failed: %s\n", strerror(errno));
    }

    // Cleanup
    for (unsigned int i = 0; i < req.count; i++) {
        munmap(buffers[i].start, buffers[i].length);
    }
    free(buffers);
    close(fd);

    printf("Captured %d frames. Stopped.\n", frame_count);
    return 0;
}
