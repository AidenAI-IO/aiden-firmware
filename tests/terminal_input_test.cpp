#include "doctest.h"

#include <string>
#include <termios.h>
#include <unistd.h>

#if defined(__APPLE__) || defined(__FreeBSD__)
#include <util.h>
#else
#include <pty.h>
#endif

namespace {

struct PtyPair {
    int master = -1;
    int slave = -1;

    PtyPair() {
        REQUIRE(openpty(&master, &slave, nullptr, nullptr, nullptr) == 0);
    }

    ~PtyPair() {
        if (master >= 0) close(master);
        if (slave >= 0) close(slave);
    }
};

std::string canonical_tty_line_after_cjk_backspace(bool enable_iutf8) {
    PtyPair pty;

    struct termios tio;
    REQUIRE(tcgetattr(pty.slave, &tio) == 0);
    tio.c_lflag |= ICANON;
    tio.c_lflag &= ~ECHO;
    tio.c_cc[VERASE] = 0x7f;
#ifdef IUTF8
    if (enable_iutf8) {
        tio.c_iflag |= IUTF8;
    } else {
        tio.c_iflag &= ~IUTF8;
    }
#else
    (void)enable_iutf8;
#endif
    REQUIRE(tcsetattr(pty.slave, TCSANOW, &tio) == 0);

    const std::string input = "你好\x7f\n";
    REQUIRE(write(pty.master, input.data(), input.size()) == static_cast<ssize_t>(input.size()));

    char buffer[64];
    ssize_t n = read(pty.slave, buffer, sizeof(buffer));
    REQUIRE(n > 0);
    return std::string(buffer, static_cast<size_t>(n));
}

}

TEST_CASE("canonical tty without IUTF8 erases only one byte of a CJK character") {
#ifdef IUTF8
    const std::string line = canonical_tty_line_after_cjk_backspace(false);

    CHECK(line != "你\n");
    CHECK(line == std::string("你") + "\xe5\xa5\n");
#else
    MESSAGE("IUTF8 is not available on this platform");
#endif
}

TEST_CASE("canonical tty with IUTF8 erases one full CJK character") {
#ifdef IUTF8
    CHECK(canonical_tty_line_after_cjk_backspace(true) == "你\n");
#else
    MESSAGE("IUTF8 is not available on this platform");
#endif
}
