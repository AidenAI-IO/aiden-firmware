#include "hid_server_html.h"
#include <arpa/inet.h>
#include <cstring>
#include <iostream>
#include <netinet/in.h>
#include <sstream>
#include <string>
#include <sys/socket.h>
#include <unistd.h>
#include <vector>

namespace {

struct HttpRequest {
    std::string method;
    std::string path;
    std::string body;
};

std::string read_until(int fd, const std::string& delimiter) {
    std::string result;
    char buf[1];
    while (true) {
        ssize_t n = read(fd, buf, 1);
        if (n <= 0) break;
        result += buf[0];
        if (result.size() >= delimiter.size() &&
            result.substr(result.size() - delimiter.size()) == delimiter) {
            break;
        }
    }
    return result;
}

HttpRequest parse_request(int client_fd) {
    HttpRequest req;

    // Read request line and headers
    std::string headers = read_until(client_fd, "\r\n\r\n");

    std::istringstream stream(headers);
    std::string line;

    // Parse request line
    if (std::getline(stream, line)) {
        std::istringstream line_stream(line);
        line_stream >> req.method >> req.path;
    }

    // Find Content-Length
    size_t content_length = 0;
    while (std::getline(stream, line)) {
        if (line.find("Content-Length:") == 0) {
            content_length = std::stoul(line.substr(15));
        }
    }

    // Read body if present
    if (content_length > 0) {
        req.body.resize(content_length);
        read(client_fd, &req.body[0], content_length);
    }

    return req;
}

void send_response(int client_fd, int status_code, const std::string& status_text,
                   const std::string& content_type, const std::string& body) {
    std::ostringstream response;
    response << "HTTP/1.1 " << status_code << " " << status_text << "\r\n";
    response << "Content-Type: " << content_type << "\r\n";
    response << "Content-Length: " << body.size() << "\r\n";
    response << "Connection: close\r\n";
    response << "\r\n";
    response << body;

    std::string resp_str = response.str();
    write(client_fd, resp_str.c_str(), resp_str.size());
}

std::string extract_json_field(const std::string& json, const std::string& field) {
    std::string search = "\"" + field + "\":";
    size_t pos = json.find(search);
    if (pos == std::string::npos) return "";

    pos += search.size();
    while (pos < json.size() && (json[pos] == ' ' || json[pos] == '\t')) pos++;

    if (pos >= json.size()) return "";

    if (json[pos] == '"') {
        // String value
        pos++;
        size_t end = json.find('"', pos);
        if (end == std::string::npos) return "";
        return json.substr(pos, end - pos);
    } else if (json[pos] == '[') {
        // Array value
        size_t end = json.find(']', pos);
        if (end == std::string::npos) return "";
        return json.substr(pos, end - pos + 1);
    } else {
        // Number value
        size_t end = pos;
        while (end < json.size() && (std::isdigit(json[end]) || json[end] == '-')) end++;
        return json.substr(pos, end - pos);
    }
}

std::vector<std::string> parse_json_array(const std::string& array_str) {
    std::vector<std::string> result;
    if (array_str.empty() || array_str[0] != '[') return result;

    size_t pos = 1;
    while (pos < array_str.size()) {
        while (pos < array_str.size() && (array_str[pos] == ' ' || array_str[pos] == ',')) pos++;
        if (pos >= array_str.size() || array_str[pos] == ']') break;

        if (array_str[pos] == '"') {
            pos++;
            size_t end = array_str.find('"', pos);
            if (end == std::string::npos) break;
            result.push_back(array_str.substr(pos, end - pos));
            pos = end + 1;
        }
    }

    return result;
}

std::string execute_hid_command(const std::string& cmd) {
    FILE* fp = popen(cmd.c_str(), "r");
    if (!fp) return "error: popen failed";

    char buf[256];
    std::string result;
    while (fgets(buf, sizeof(buf), fp)) {
        result += buf;
    }

    int status = pclose(fp);
    if (status != 0) {
        return "error: command failed with status " + std::to_string(status);
    }

    return result.empty() ? "ok" : result;
}

std::string handle_api_request(const std::string& path, const std::string& body,
                               const std::string& hid_binary) {
    if (path == "/api/keyboard/tap") {
        std::string keys_str = extract_json_field(body, "keys");
        std::vector<std::string> keys = parse_json_array(keys_str);

        if (keys.empty()) {
            return "{\"error\": \"missing keys\"}";
        }

        std::string cmd = hid_binary + " keyboard tap";
        for (const auto& key : keys) {
            cmd += " " + key;
        }

        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    if (path == "/api/keyboard/text") {
        std::string text = extract_json_field(body, "text");
        if (text.empty()) {
            return "{\"error\": \"missing text\"}";
        }

        std::string cmd = hid_binary + " keyboard text '" + text + "'";
        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    if (path == "/api/touch/move") {
        std::string x = extract_json_field(body, "x");
        std::string y = extract_json_field(body, "y");

        if (x.empty() || y.empty()) {
            return "{\"error\": \"missing x or y\"}";
        }

        std::string cmd = hid_binary + " touch move " + x + " " + y;
        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    if (path == "/api/touch/tap") {
        std::string x = extract_json_field(body, "x");
        std::string y = extract_json_field(body, "y");

        if (x.empty() || y.empty()) {
            return "{\"error\": \"missing x or y\"}";
        }

        std::string cmd = hid_binary + " touch tap " + x + " " + y;
        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    if (path == "/api/touch/click") {
        std::string button = extract_json_field(body, "button");
        if (button.empty()) button = "left";

        std::string cmd = hid_binary + " touch click " + button;
        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    if (path == "/api/touch/scroll") {
        std::string amount = extract_json_field(body, "amount");
        if (amount.empty()) {
            return "{\"error\": \"missing amount\"}";
        }

        std::string cmd = hid_binary + " touch scroll " + amount;
        std::string result = execute_hid_command(cmd);
        return "{\"result\": \"" + result + "\"}";
    }

    return "{\"error\": \"unknown endpoint\"}";
}

}  // namespace

void run_hid_server(int port, const std::string& hid_binary) {
    int server_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd < 0) {
        std::cerr << "Failed to create socket" << std::endl;
        return;
    }

    int opt = 1;
    setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    struct sockaddr_in addr;
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(port);

    if (bind(server_fd, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
        std::cerr << "Failed to bind to port " << port << std::endl;
        close(server_fd);
        return;
    }

    if (listen(server_fd, 10) < 0) {
        std::cerr << "Failed to listen" << std::endl;
        close(server_fd);
        return;
    }

    std::cout << "HID test server running on http://0.0.0.0:" << port << std::endl;
    std::cout << "Press Ctrl+C to stop" << std::endl;

    while (true) {
        struct sockaddr_in client_addr;
        socklen_t client_len = sizeof(client_addr);
        int client_fd = accept(server_fd, (struct sockaddr*)&client_addr, &client_len);

        if (client_fd < 0) {
            continue;
        }

        HttpRequest req = parse_request(client_fd);

        if (req.method == "GET" && req.path == "/") {
            send_response(client_fd, 200, "OK", "text/html", HID_SERVER_HTML);
        } else if (req.method == "POST" && req.path.find("/api/") == 0) {
            std::string response = handle_api_request(req.path, req.body, hid_binary);
            send_response(client_fd, 200, "OK", "application/json", response);
        } else {
            send_response(client_fd, 404, "Not Found", "text/plain", "Not Found");
        }

        close(client_fd);
    }

    close(server_fd);
}
