#include "doctest.h"
#include "wifi_config.h"

#include <fstream>
#include <iterator>
#include <string>
#include <unistd.h>

namespace {

struct TempWifiFile {
    std::string path;

    TempWifiFile() {
        char tmpl[] = "/tmp/aiden_wifi_XXXXXX";
        int fd = mkstemp(tmpl);
        REQUIRE(fd >= 0);
        ::close(fd);
        path = tmpl;
    }

    ~TempWifiFile() { ::unlink(path.c_str()); }
};

std::string read_text_file(const std::string& path) {
    std::ifstream input(path.c_str(), std::ios::binary);
    std::string contents;
    contents.assign(std::istreambuf_iterator<char>(input), std::istreambuf_iterator<char>());
    return contents;
}

}

TEST_CASE("save_wifi_config hex-encodes ssid and quotes psk") {
    TempWifiFile file;
    aiden::WifiNetworkConfig wifi;
    wifi.ssid = "小明の家";
    wifi.psk = "password123";
    wifi.country = "CN";

    std::string err;
    REQUIRE(aiden::save_wifi_config(file.path.c_str(), wifi, &err));
    CHECK(err.empty());

    std::string contents = read_text_file(file.path);
    CHECK(contents.find("ssid=e5b08fe6988ee381aee5aeb6") != std::string::npos);
    CHECK(contents.find("psk=\"password123\"") != std::string::npos);
    CHECK(contents.find("country=CN") != std::string::npos);
}

TEST_CASE("load_wifi_config decodes generated config") {
    TempWifiFile file;
    aiden::WifiNetworkConfig out;
    out.ssid = "Cafe WiFi";
    out.psk = "raw-password";
    out.country = "US";
    REQUIRE(aiden::save_wifi_config(file.path.c_str(), out));

    aiden::WifiNetworkConfig in;
    std::string err;
    REQUIRE(aiden::load_wifi_config(file.path.c_str(), in, &err));
    CHECK(err.empty());
    CHECK(in.ssid == "Cafe WiFi");
    CHECK(in.psk == "raw-password");
    CHECK(in.country == "US");
}

TEST_CASE("save_wifi_config writes open network when psk is empty") {
    TempWifiFile file;
    aiden::WifiNetworkConfig wifi;
    wifi.ssid = "OpenNet";

    REQUIRE(aiden::save_wifi_config(file.path.c_str(), wifi));
    std::string contents = read_text_file(file.path);
    CHECK(contents.find("key_mgmt=NONE") != std::string::npos);
}
