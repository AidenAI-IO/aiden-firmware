#pragma once
#include <string>
#include <vector>
#include <cstdint>

struct ScreenshotData {
    std::vector<uint8_t> jpeg;
    int width = 0;
    int height = 0;
};

void run_hid_server(int port, const std::vector<std::string>& hid_command_prefix);
