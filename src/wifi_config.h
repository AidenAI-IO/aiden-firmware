#pragma once

#include <string>
#include <vector>

namespace aiden {

struct WifiNetwork {
    std::string ssid;
    std::string psk;
    int priority;
    bool scan_ssid;
    bool disabled;

    WifiNetwork() : priority(0), scan_ssid(true), disabled(false) {}
};

struct WifiNetworkConfig {
    std::string ssid;
    std::string psk;
    std::string country;
    std::vector<WifiNetwork> networks;

    WifiNetworkConfig() {}
};

bool load_wifi_config(const char* path, WifiNetworkConfig& config, std::string* error = nullptr);
bool save_wifi_config(const char* path, const WifiNetworkConfig& config, std::string* error = nullptr);
std::string render_wifi_config(const WifiNetworkConfig& config);
int find_wifi_network_index(const WifiNetworkConfig& config, const std::string& ssid);
bool upsert_wifi_network(WifiNetworkConfig* config, const WifiNetwork& network);
bool remove_wifi_network(WifiNetworkConfig* config, const std::string& ssid);
bool promote_wifi_network(WifiNetworkConfig* config, const std::string& ssid);
void normalize_wifi_priorities(WifiNetworkConfig* config);
void sync_legacy_wifi_fields(WifiNetworkConfig* config);
std::string detect_country_from_ap(const std::string& interface = "wlan0");
std::string detect_country_by_ssid(const std::string& ssid, const std::string& interface = "wlan0");

}
