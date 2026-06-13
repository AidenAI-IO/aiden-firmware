#pragma once

#include <string>

namespace aiden {

struct WifiNetworkConfig {
    std::string ssid;
    std::string psk;
    std::string country;

    WifiNetworkConfig() {}
};

bool load_wifi_config(const char* path, WifiNetworkConfig& config, std::string* error = nullptr);
bool save_wifi_config(const char* path, const WifiNetworkConfig& config, std::string* error = nullptr);
std::string render_wifi_config(const WifiNetworkConfig& config);
std::string detect_country_from_ap(const std::string& interface = "wlan0");
std::string detect_country_by_ssid(const std::string& ssid, const std::string& interface = "wlan0");

}
