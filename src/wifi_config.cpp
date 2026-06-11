#include "wifi_config.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

namespace aiden {

namespace {

void set_error(std::string* error, const std::string& msg) {
    if (error) {
        *error = msg;
    }
}

std::string trim_copy(const std::string& input) {
    size_t start = 0;
    while (start < input.size() && isspace(static_cast<unsigned char>(input[start]))) {
        ++start;
    }

    size_t end = input.size();
    while (end > start && isspace(static_cast<unsigned char>(input[end - 1]))) {
        --end;
    }

    return input.substr(start, end - start);
}

bool is_hex_digit(char ch) {
    return (ch >= '0' && ch <= '9') ||
           (ch >= 'a' && ch <= 'f') ||
           (ch >= 'A' && ch <= 'F');
}

int hex_value(char ch) {
    if (ch >= '0' && ch <= '9') {
        return ch - '0';
    }
    if (ch >= 'a' && ch <= 'f') {
        return 10 + ch - 'a';
    }
    if (ch >= 'A' && ch <= 'F') {
        return 10 + ch - 'A';
    }
    return -1;
}

bool looks_like_hex(const std::string& text) {
    if (text.empty() || (text.size() % 2) != 0) {
        return false;
    }
    for (size_t i = 0; i < text.size(); ++i) {
        if (!is_hex_digit(text[i])) {
            return false;
        }
    }
    return true;
}

std::string hex_encode(const std::string& bytes) {
    static const char kHex[] = "0123456789abcdef";
    std::string out;
    out.reserve(bytes.size() * 2);
    for (size_t i = 0; i < bytes.size(); ++i) {
        unsigned char ch = static_cast<unsigned char>(bytes[i]);
        out.push_back(kHex[(ch >> 4) & 0x0f]);
        out.push_back(kHex[ch & 0x0f]);
    }
    return out;
}

bool hex_decode(const std::string& text, std::string* out) {
    if (!out || !looks_like_hex(text)) {
        return false;
    }

    out->clear();
    out->reserve(text.size() / 2);
    for (size_t i = 0; i < text.size(); i += 2) {
        int hi = hex_value(text[i]);
        int lo = hex_value(text[i + 1]);
        if (hi < 0 || lo < 0) {
            return false;
        }
        out->push_back(static_cast<char>((hi << 4) | lo));
    }
    return true;
}

std::string parse_quoted_value(const std::string& text) {
    if (text.size() < 2 || text[0] != '"' || text[text.size() - 1] != '"') {
        return text;
    }

    std::string out;
    out.reserve(text.size() - 2);
    for (size_t i = 1; i + 1 < text.size(); ++i) {
        char ch = text[i];
        if (ch == '\\' && i + 2 < text.size()) {
            ++i;
            out.push_back(text[i]);
        } else {
            out.push_back(ch);
        }
    }
    return out;
}

std::string quote_value(const std::string& text) {
    std::string out = "\"";
    out.reserve(text.size() + 2);
    for (size_t i = 0; i < text.size(); ++i) {
        char ch = text[i];
        if (ch == '\\' || ch == '"') {
            out.push_back('\\');
        }
        out.push_back(ch);
    }
    out.push_back('"');
    return out;
}

bool is_raw_psk(const std::string& psk) {
    return psk.size() == 64 && looks_like_hex(psk);
}

bool read_file(const char* path, std::string* out, std::string* error) {
    if (!path) {
        set_error(error, "cannot open wifi config: path is null");
        return false;
    }

    FILE* fp = fopen(path, "r");
    if (!fp) {
        int saved_errno = errno;
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return false;
    }

    out->clear();
    char buffer[1024];
    while (fgets(buffer, sizeof(buffer), fp)) {
        out->append(buffer);
    }

    if (ferror(fp)) {
        int saved_errno = errno;
        fclose(fp);
        char buf[512];
        snprintf(buf, sizeof(buf), "failed to read '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return false;
    }

    fclose(fp);
    return true;
}

FILE* open_secure_write_file(const char* path, std::string* error) {
    int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, S_IRUSR | S_IWUSR);
    if (fd < 0) {
        int saved_errno = errno;
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s' for write: %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    if (fchmod(fd, S_IRUSR | S_IWUSR) != 0) {
        int saved_errno = errno;
        close(fd);
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot set permissions on '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    FILE* fp = fdopen(fd, "w");
    if (!fp) {
        int saved_errno = errno;
        close(fd);
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s' stream for write: %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    return fp;
}

bool write_file(const char* path, const std::string& text, std::string* error) {
    if (!path) {
        set_error(error, "cannot save wifi config: path is null");
        return false;
    }

    FILE* fp = open_secure_write_file(path, error);
    if (!fp) {
        return false;
    }

    size_t written = fwrite(text.data(), 1, text.size(), fp);
    int saved_errno = errno;
    if (fclose(fp) != 0 && saved_errno == 0) {
        saved_errno = errno;
    }

    if (written != text.size()) {
        char buf[512];
        snprintf(buf, sizeof(buf), "failed to write '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return false;
    }

    return true;
}

bool parse_assignment(const std::string& line, const char* key, std::string* value) {
    const std::string prefix = std::string(key) + "=";
    if (line.compare(0, prefix.size(), prefix) != 0) {
        return false;
    }
    *value = trim_copy(line.substr(prefix.size()));
    return true;
}

}

std::string render_wifi_config(const WifiNetworkConfig& config) {
    std::string out;
    out += "ctrl_interface=/var/run/wpa_supplicant\n";
    out += "update_config=1\n";
    out += "country=";
    if (config.country.empty()) {
        // Try to detect country from the target SSID before connecting
        std::string detected = detect_country_by_ssid(config.ssid);
        if (detected.empty()) {
            // Fallback: try from currently connected AP
            detected = detect_country_from_ap();
        }
        out += detected.empty() ? "CN" : detected;
    } else {
        out += config.country;
    }
    out += "\n\n";
    out += "network={\n";
    out += "    ssid=";
    out += hex_encode(config.ssid);
    out += "\n";
    if (config.psk.empty()) {
        out += "    key_mgmt=NONE\n";
    } else if (is_raw_psk(config.psk)) {
        out += "    psk=";
        out += config.psk;
        out += "\n";
    } else {
        out += "    psk=";
        out += quote_value(config.psk);
        out += "\n";
    }
    out += "    scan_ssid=1\n";
    out += "}\n";
    return out;
}

bool load_wifi_config(const char* path, WifiNetworkConfig& config, std::string* error) {
    if (error) error->clear();

    std::string text;
    if (!read_file(path, &text, error)) {
        return false;
    }

    WifiNetworkConfig parsed;
    size_t pos = 0;
    while (pos < text.size()) {
        size_t next = text.find('\n', pos);
        std::string line = text.substr(pos, next == std::string::npos ? std::string::npos : next - pos);
        line = trim_copy(line);

        std::string value;
        if (parse_assignment(line, "country", &value)) {
            parsed.country = value;
        } else if (parse_assignment(line, "ssid", &value)) {
            std::string decoded;
            if (hex_decode(value, &decoded)) {
                parsed.ssid = decoded;
            } else {
                parsed.ssid = parse_quoted_value(value);
            }
        } else if (parse_assignment(line, "psk", &value)) {
            if (looks_like_hex(value) && value.size() == 64) {
                parsed.psk = value;
            } else {
                parsed.psk = parse_quoted_value(value);
            }
        }

        if (next == std::string::npos) {
            break;
        }
        pos = next + 1;
    }

    config = parsed;
    if (config.country.empty()) {
        // Try to detect from target SSID first (works before connecting)
        config.country = detect_country_by_ssid(config.ssid);
        if (config.country.empty()) {
            // Fallback: try from currently connected AP
            config.country = detect_country_from_ap();
        }
        if (config.country.empty()) {
            config.country = "CN";  // Last resort fallback
        }
    }
    return true;
}

std::string detect_country_by_ssid(const std::string& ssid, const std::string& interface) {
    // Scan and extract the Country IE of the BSS whose SSID matches `ssid`.
    // Works BEFORE associating, avoiding the connect-to-learn-country deadlock.
    //
    // Strategy: in iw scan output, within a BSS block the Country line appears
    // after the SSID line. Set a flag when the target SSID is seen, then emit
    // the next Country line encountered before the next "BSS" header.
    //
    // SSID is passed via awk -v (not interpolated into a regex) to handle
    // values with regex metacharacters or quotes safely.
    std::string safe_ssid;
    for (char c : ssid) {
        if (c == '\'') {
            safe_ssid += "'\\''";  // shell escape: close-quote, literal quote, reopen
        } else {
            safe_ssid += c;
        }
    }

    std::string awk_prog =
        "/^BSS /{found=0} "
        "/SSID: /{s=$0; sub(/.*SSID: /,\"\",s); if(s==target) found=1} "
        "found && /Country:/{print $2; exit}";

    std::string cmd = "iw dev " + interface + " scan 2>/dev/null | awk -v target='" +
                      safe_ssid + "' '" + awk_prog + "'";

    FILE* pipe = popen(cmd.c_str(), "r");
    if (!pipe) {
        return "";
    }

    char buffer[16];
    std::string country;
    if (fgets(buffer, sizeof(buffer), pipe) != nullptr) {
        country = trim_copy(std::string(buffer));
        // Validate country code (2 uppercase letters)
        if (country.size() == 2 &&
            isupper(country[0]) && isupper(country[1])) {
            pclose(pipe);
            return country;
        }
    }
    pclose(pipe);

    return "";
}

std::string detect_country_from_ap(const std::string& interface) {
    // Try to detect country code from the currently connected AP
    std::string cmd = "iw dev " + interface + " scan 2>/dev/null | grep -A 1 '(on " + interface + ")' | grep 'Country:' | head -1 | awk '{print $2}'";

    FILE* pipe = popen(cmd.c_str(), "r");
    if (!pipe) {
        return "";
    }

    char buffer[16];
    std::string country;
    if (fgets(buffer, sizeof(buffer), pipe) != nullptr) {
        country = trim_copy(std::string(buffer));
        // Validate country code (2 uppercase letters)
        if (country.size() == 2 &&
            isupper(country[0]) && isupper(country[1])) {
            pclose(pipe);
            return country;
        }
    }
    pclose(pipe);

    // If detection failed, try reading from wpa_supplicant status
    cmd = "wpa_cli -i " + interface + " status 2>/dev/null | grep '^country=' | cut -d= -f2";
    pipe = popen(cmd.c_str(), "r");
    if (!pipe) {
        return "";
    }

    if (fgets(buffer, sizeof(buffer), pipe) != nullptr) {
        country = trim_copy(std::string(buffer));
        if (country.size() == 2 &&
            isupper(country[0]) && isupper(country[1])) {
            pclose(pipe);
            return country;
        }
    }
    pclose(pipe);

    return "";  // Detection failed
}

bool save_wifi_config(const char* path, const WifiNetworkConfig& config, std::string* error) {
    if (error) error->clear();
    return write_file(path, render_wifi_config(config), error);
}

}
