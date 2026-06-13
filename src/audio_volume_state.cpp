#include "audio_volume_state.h"

#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <string>
#include <sys/stat.h>
#include <unistd.h>

namespace aiden {

namespace {

void set_error(std::string* error, const std::string& msg) {
    if (error) {
        *error = msg;
    }
}

std::string parent_dir(const std::string& path) {
    size_t slash = path.find_last_of('/');
    if (slash == std::string::npos) return ".";
    if (slash == 0) return "/";
    return path.substr(0, slash);
}

bool mkdir_p(const std::string& path, mode_t mode, std::string* error) {
    if (path.empty() || path == ".") return true;
    if (path == "/") return true;

    std::string current;
    size_t pos = 0;
    if (path[0] == '/') {
        current = "/";
        pos = 1;
    }

    while (pos <= path.size()) {
        size_t next = path.find('/', pos);
        std::string part = path.substr(pos, next == std::string::npos ? std::string::npos : next - pos);
        if (!part.empty()) {
            if (!current.empty() && current[current.size() - 1] != '/') current += "/";
            current += part;
            if (::mkdir(current.c_str(), mode) != 0) {
                if (errno != EEXIST) {
                    set_error(error, "mkdir " + current + ": " + std::strerror(errno));
                    return false;
                }
            } else if (::chmod(current.c_str(), mode) != 0) {
                set_error(error, "chmod " + current + ": " + std::strerror(errno));
                return false;
            }
        }
        if (next == std::string::npos) break;
        pos = next + 1;
    }
    return true;
}

bool write_all(int fd, const char* data, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = ::write(fd, data + off, len - off);
        if (n < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        if (n == 0) return false;
        off += static_cast<size_t>(n);
    }
    return true;
}

}  // namespace

AudioVolumeStateLoadStatus load_playback_volume_state(const char* path,
                                                      int* volume,
                                                      std::string* error) {
    if (error) error->clear();
    if (!path || path[0] == '\0') {
        set_error(error, "volume state path is empty");
        return AudioVolumeStateLoadStatus::ERROR;
    }
    if (!volume) {
        set_error(error, "volume output is null");
        return AudioVolumeStateLoadStatus::ERROR;
    }

    FILE* fp = std::fopen(path, "r");
    if (!fp) {
        if (errno == ENOENT) {
            return AudioVolumeStateLoadStatus::MISSING;
        }
        set_error(error, std::string("open ") + path + ": " + std::strerror(errno));
        return AudioVolumeStateLoadStatus::ERROR;
    }

    char buf[64] = {0};
    errno = 0;
    if (!std::fgets(buf, sizeof(buf), fp)) {
        int saved_errno = errno;
        std::fclose(fp);
        if (saved_errno != 0) {
            set_error(error, std::string("read ") + path + ": " + std::strerror(saved_errno));
            return AudioVolumeStateLoadStatus::ERROR;
        }
        set_error(error, "volume state is empty");
        return AudioVolumeStateLoadStatus::INVALID;
    }

    char extra[2] = {0};
    bool too_long = std::fgets(extra, sizeof(extra), fp) != nullptr;
    std::fclose(fp);
    if (too_long) {
        set_error(error, "volume state has trailing content");
        return AudioVolumeStateLoadStatus::INVALID;
    }

    errno = 0;
    char* end = nullptr;
    long value = std::strtol(buf, &end, 10);
    if (errno != 0 || end == buf) {
        set_error(error, "volume state is not an integer");
        return AudioVolumeStateLoadStatus::INVALID;
    }
    while (*end == ' ' || *end == '\t' || *end == '\r' || *end == '\n') {
        ++end;
    }
    if (*end != '\0') {
        set_error(error, "volume state has invalid trailing characters");
        return AudioVolumeStateLoadStatus::INVALID;
    }
    if (value < 0 || value > 100) {
        set_error(error, "volume state is out of range 0..100");
        return AudioVolumeStateLoadStatus::INVALID;
    }

    *volume = static_cast<int>(value);
    return AudioVolumeStateLoadStatus::LOADED;
}

bool save_playback_volume_state(const char* path,
                                int volume,
                                std::string* error) {
    if (error) error->clear();
    if (!path || path[0] == '\0') {
        set_error(error, "volume state path is empty");
        return false;
    }
    if (volume < 0 || volume > 100) {
        set_error(error, "volume must be in range 0..100");
        return false;
    }

    std::string state_path(path);
    if (!mkdir_p(parent_dir(state_path), 0755, error)) {
        return false;
    }

    std::string tmp = state_path + ".tmp";
    int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
    if (fd < 0) {
        set_error(error, "open " + tmp + ": " + std::strerror(errno));
        return false;
    }
    if (::fchmod(fd, 0644) != 0) {
        set_error(error, "chmod " + tmp + ": " + std::strerror(errno));
        ::close(fd);
        ::unlink(tmp.c_str());
        return false;
    }

    char buf[16];
    int len = std::snprintf(buf, sizeof(buf), "%d\n", volume);
    if (len <= 0 || static_cast<size_t>(len) >= sizeof(buf) ||
        !write_all(fd, buf, static_cast<size_t>(len))) {
        set_error(error, "write " + tmp + ": " + std::strerror(errno));
        ::close(fd);
        ::unlink(tmp.c_str());
        return false;
    }

    if (::fsync(fd) != 0) {
        set_error(error, "fsync " + tmp + ": " + std::strerror(errno));
        ::close(fd);
        ::unlink(tmp.c_str());
        return false;
    }
    if (::close(fd) != 0) {
        set_error(error, "close " + tmp + ": " + std::strerror(errno));
        ::unlink(tmp.c_str());
        return false;
    }
    if (::rename(tmp.c_str(), state_path.c_str()) != 0) {
        set_error(error, "rename " + tmp + " -> " + state_path + ": " + std::strerror(errno));
        ::unlink(tmp.c_str());
        return false;
    }

    std::string dir = parent_dir(state_path);
    int dirfd = ::open(dir.c_str(), O_RDONLY | O_DIRECTORY);
    if (dirfd < 0) {
        set_error(error, "open dir " + dir + ": " + std::strerror(errno));
        return false;
    }
    if (::fsync(dirfd) != 0) {
        set_error(error, "fsync dir " + dir + ": " + std::strerror(errno));
        ::close(dirfd);
        return false;
    }
    if (::close(dirfd) != 0) {
        set_error(error, "close dir " + dir + ": " + std::strerror(errno));
        return false;
    }

    return true;
}

}  // namespace aiden
