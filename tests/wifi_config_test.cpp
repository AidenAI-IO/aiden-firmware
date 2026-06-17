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

TEST_CASE("load_wifi_config parses multiple saved networks") {
    TempWifiFile file;
    {
        std::ofstream out(file.path.c_str(), std::ios::binary);
        out << "ctrl_interface=/var/run/wpa_supplicant\n"
            << "update_config=1\n"
            << "country=US\n\n"
            << "network={\n"
            << "    ssid=\"xxx\"\n"
            << "    psk=\"xxx-password\"\n"
            << "    scan_ssid=1\n"
            << "    priority=1\n"
            << "}\n\n"
            << "network={\n"
            << "    ssid=797979\n"
            << "    psk=\"yyy-password\"\n"
            << "    priority=2\n"
            << "}\n";
    }

    aiden::WifiNetworkConfig wifi;
    std::string err;
    REQUIRE(aiden::load_wifi_config(file.path.c_str(), wifi, &err));
    CHECK(err.empty());
    REQUIRE(wifi.networks.size() == 2);
    CHECK(wifi.networks[0].ssid == "xxx");
    CHECK(wifi.networks[0].psk == "xxx-password");
    CHECK(wifi.networks[0].priority == 1);
    CHECK(wifi.networks[1].ssid == "yyy");
    CHECK(wifi.networks[1].psk == "yyy-password");
    CHECK(wifi.networks[1].priority == 2);
    CHECK(wifi.ssid == "yyy");
    CHECK(wifi.psk == "yyy-password");
}

TEST_CASE("save_wifi_config renders multiple networks with priorities") {
    TempWifiFile file;
    aiden::WifiNetworkConfig wifi;
    wifi.country = "CN";
    aiden::WifiNetwork xxx;
    xxx.ssid = "xxx";
    xxx.psk = "xxx-password";
    xxx.priority = 1;
    aiden::WifiNetwork yyy;
    yyy.ssid = "yyy";
    yyy.psk = "yyy-password";
    yyy.priority = 2;
    wifi.networks.push_back(xxx);
    wifi.networks.push_back(yyy);

    REQUIRE(aiden::save_wifi_config(file.path.c_str(), wifi));
    std::string contents = read_text_file(file.path);
    CHECK(contents.find("ssid=787878") != std::string::npos);
    CHECK(contents.find("psk=\"xxx-password\"") != std::string::npos);
    CHECK(contents.find("priority=1") != std::string::npos);
    CHECK(contents.find("ssid=797979") != std::string::npos);
    CHECK(contents.find("psk=\"yyy-password\"") != std::string::npos);
    CHECK(contents.find("priority=2") != std::string::npos);
}

TEST_CASE("promote_wifi_network makes last successful ssid highest priority") {
    aiden::WifiNetworkConfig wifi;
    aiden::WifiNetwork xxx;
    xxx.ssid = "xxx";
    xxx.priority = 1;
    aiden::WifiNetwork yyy;
    yyy.ssid = "yyy";
    yyy.priority = 2;
    wifi.networks.push_back(xxx);
    wifi.networks.push_back(yyy);

    REQUIRE(aiden::promote_wifi_network(&wifi, "xxx"));
    REQUIRE(wifi.networks.size() == 2);
    CHECK(wifi.networks[0].ssid == "yyy");
    CHECK(wifi.networks[0].priority == 1);
    CHECK(wifi.networks[1].ssid == "xxx");
    CHECK(wifi.networks[1].priority == 2);
    CHECK(wifi.ssid == "xxx");
}

TEST_CASE("upsert and remove wifi network preserve other saved networks") {
    aiden::WifiNetworkConfig wifi;
    aiden::WifiNetwork xxx;
    xxx.ssid = "xxx";
    xxx.psk = "old";
    xxx.priority = 1;
    REQUIRE(aiden::upsert_wifi_network(&wifi, xxx));

    aiden::WifiNetwork yyy;
    yyy.ssid = "yyy";
    yyy.psk = "new";
    yyy.priority = 2;
    REQUIRE(aiden::upsert_wifi_network(&wifi, yyy));
    REQUIRE(wifi.networks.size() == 2);
    CHECK(aiden::find_wifi_network_index(wifi, "xxx") >= 0);
    CHECK(aiden::find_wifi_network_index(wifi, "yyy") >= 0);

    REQUIRE(aiden::remove_wifi_network(&wifi, "xxx"));
    REQUIRE(wifi.networks.size() == 1);
    CHECK(wifi.networks[0].ssid == "yyy");
    CHECK(aiden::find_wifi_network_index(wifi, "xxx") < 0);
}

TEST_CASE("removing the last wifi network clears legacy credentials") {
    aiden::WifiNetworkConfig wifi;
    aiden::WifiNetwork network;
    network.ssid = "obsolete";
    network.psk = "old-password";
    network.priority = 1;
    REQUIRE(aiden::upsert_wifi_network(&wifi, network));
    CHECK(wifi.ssid == "obsolete");
    CHECK(wifi.psk == "old-password");

    REQUIRE(aiden::remove_wifi_network(&wifi, "obsolete"));
    CHECK(wifi.networks.empty());
    CHECK(wifi.ssid.empty());
    CHECK(wifi.psk.empty());

    std::string rendered = aiden::render_wifi_config(wifi);
    CHECK(rendered.find("network={") == std::string::npos);
    CHECK(rendered.find("obsolete") == std::string::npos);
}

TEST_CASE("sync_legacy_wifi_fields clears legacy credentials for empty networks") {
    aiden::WifiNetworkConfig wifi;
    wifi.ssid = "legacy";
    wifi.psk = "legacy-password";

    aiden::sync_legacy_wifi_fields(&wifi);

    CHECK(wifi.networks.empty());
    CHECK(wifi.ssid.empty());
    CHECK(wifi.psk.empty());
}
