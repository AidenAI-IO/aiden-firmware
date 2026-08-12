#include "doctest.h"
#include "frame_ipc.h"
#include "frame_service_client.h"
#include "frame_service_server.h"
#include "cJSON/cJSON.h"
#include <atomic>
#include <chrono>
#include <cstring>
#include <string>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <thread>
#include <unistd.h>
#include <vector>

using aiden::FrameMetadata;
using aiden::FrameResult;
using aiden::FrameServiceClient;
using aiden::FrameServiceServer;
using aiden::FrameServiceStatus;
using aiden::HealthResult;

namespace {

struct TempSocketPath {
    std::string path;

    TempSocketPath() {
        char tmpl[] = "/tmp/aiden_frame_server_XXXXXX";
        int fd = ::mkstemp(tmpl);
        REQUIRE(fd >= 0);
        ::close(fd);
        ::unlink(tmpl);
        path = tmpl;
    }

    ~TempSocketPath() { ::unlink(path.c_str()); }
};

FrameMetadata metadata(uint32_t width, uint32_t height, const char* format, uint64_t ts) {
    FrameMetadata meta;
    meta.capture_ts_ns = ts;
    meta.width = width;
    meta.height = height;
    meta.pixel_format = format;
    meta.stride = width * 2;
    return meta;
}

uint64_t monotonic_ns() {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return static_cast<uint64_t>(ts.tv_sec) * 1000000000ULL + static_cast<uint64_t>(ts.tv_nsec);
}

std::vector<uint8_t> payload(std::initializer_list<uint8_t> values) {
    return std::vector<uint8_t>(values);
}

int connect_raw_client(const std::string& path) {
    int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
    REQUIRE(fd >= 0);
    struct sockaddr_un addr;
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    strncpy(addr.sun_path, path.c_str(), sizeof(addr.sun_path) - 1);
    REQUIRE(::connect(fd, reinterpret_cast<struct sockaddr*>(&addr), sizeof(addr)) == 0);
    return fd;
}

void read_exact_or_fail(int fd, uint8_t* data, size_t len) {
    while (len > 0) {
        ssize_t got = ::read(fd, data, len);
        REQUIRE(got > 0);
        data += got;
        len -= static_cast<size_t>(got);
    }
}

void request_latest_frame_raw(int fd) {
    REQUIRE(aiden::write_frame_message(fd,
                                       "{\"type\":\"request\",\"method\":\"latest_frame\",\"since_seq\":\"0\",\"timeout_ms\":0}",
                                       std::vector<uint8_t>()) == FrameServiceStatus::OK);
}

void request_health_raw(int fd) {
    REQUIRE(aiden::write_frame_message(fd,
                                       "{\"type\":\"request\",\"method\":\"health\"}",
                                       std::vector<uint8_t>()) == FrameServiceStatus::OK);
}

void read_response_header(int fd, aiden::FrameWirePrefix* prefix) {
    uint8_t prefix_bytes[aiden::kFrameWirePrefixSize];
    read_exact_or_fail(fd, prefix_bytes, sizeof(prefix_bytes));
    REQUIRE(aiden::decode_frame_wire_prefix(prefix_bytes, sizeof(prefix_bytes), prefix));
    std::vector<uint8_t> header(prefix->header_len);
    read_exact_or_fail(fd, header.data(), header.size());
}

std::string read_response(int fd, std::vector<uint8_t>* payload_out) {
    aiden::FrameWirePrefix prefix;
    uint8_t prefix_bytes[aiden::kFrameWirePrefixSize];
    read_exact_or_fail(fd, prefix_bytes, sizeof(prefix_bytes));
    REQUIRE(aiden::decode_frame_wire_prefix(prefix_bytes, sizeof(prefix_bytes), &prefix));
    std::vector<uint8_t> header(prefix.header_len);
    read_exact_or_fail(fd, header.data(), header.size());
    payload_out->resize(static_cast<size_t>(prefix.payload_len));
    if (!payload_out->empty()) {
        read_exact_or_fail(fd, payload_out->data(), payload_out->size());
    }
    return std::string(header.begin(), header.end());
}

bool fd_readable_within(int fd, uint32_t timeout_ms) {
    fd_set read_set;
    FD_ZERO(&read_set);
    FD_SET(fd, &read_set);
    struct timeval timeout;
    timeout.tv_sec = timeout_ms / 1000;
    timeout.tv_usec = (timeout_ms % 1000) * 1000;
    int ready = ::select(fd + 1, &read_set, nullptr, nullptr, &timeout);
    return ready > 0 && FD_ISSET(fd, &read_set);
}

}

TEST_CASE("FrameServiceServer serves health over UDS") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    FrameServiceClient client(socket_path.path.c_str());
    HealthResult health;

    REQUIRE(client.health(&health) == FrameServiceStatus::OK);
    CHECK(health.state == "RUNNING");
    CHECK(health.latest_seq == 0);
    CHECK(health.ring_buffer_size == 4);
    CHECK(health.ring_buffer_used == 0);

    server.stop();
}

TEST_CASE("FrameServiceServer health reports frame age and serve latency") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = payload({1, 2});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", monotonic_ns() - 20ULL * 1000000ULL),
                                data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(client.latest_frame(0, 0, &frame) == FrameServiceStatus::OK);

    HealthResult health;
    REQUIRE(client.health(&health) == FrameServiceStatus::OK);
    CHECK(health.latest_seq == seq);
    CHECK(health.frame_age_ms >= 1);
    CHECK(health.ring_buffer_used == 1);
    CHECK(health.avg_frame_serve_latency_ms > 0.0);

    server.stop();
}

TEST_CASE("FrameServiceServer health emits numeric counters and string recovery timestamp") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    std::vector<uint8_t> data = payload({1, 2});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", monotonic_ns() - 20ULL * 1000000ULL),
                                data.data(), data.size(), &seq) == FrameServiceStatus::OK);
    server.record_recovery("recoverable", true);

    int fd = connect_raw_client(socket_path.path);
    request_health_raw(fd);

    aiden::FrameIpcMessage response;
    REQUIRE(aiden::read_frame_message(fd, &response) == FrameServiceStatus::OK);
    CHECK(response.payload.empty());

    cJSON* root = cJSON_Parse(response.header_json.c_str());
    REQUIRE(root != nullptr);

    cJSON* latest_seq = cJSON_GetObjectItem(root, "latest_seq");
    REQUIRE(latest_seq != nullptr);
    CHECK((latest_seq->type & 0xff) == cJSON_Number);
    CHECK(static_cast<uint64_t>(latest_seq->valuedouble) == seq);

    cJSON* frame_age_ms = cJSON_GetObjectItem(root, "frame_age_ms");
    REQUIRE(frame_age_ms != nullptr);
    CHECK((frame_age_ms->type & 0xff) == cJSON_Number);
    CHECK(frame_age_ms->valuedouble >= 1.0);

    cJSON* last_recovery_ts = cJSON_GetObjectItem(root, "last_recovery_ts");
    REQUIRE(last_recovery_ts != nullptr);
    CHECK((last_recovery_ts->type & 0xff) == cJSON_String);
    REQUIRE(last_recovery_ts->valuestring != nullptr);
    CHECK(last_recovery_ts->valuestring[0] != '\0');

    cJSON_Delete(root);
    ::close(fd);
    server.stop();
}

TEST_CASE("FrameServiceServer health escapes control characters in last_error") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::string error = std::string("line\nnext\t") + static_cast<char>(1) + "\"\\end";
    server.record_recovery(error, true);

    FrameServiceClient client(socket_path.path.c_str());
    HealthResult health;
    REQUIRE(client.health(&health) == FrameServiceStatus::OK);
    CHECK(health.last_error == error);

    server.stop();
}

TEST_CASE("FrameServiceServer returns latest frame payload") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = payload({1, 2, 3, 4});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(2, 1, "uyvy", 99), data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(client.latest_frame(0, 0, &frame) == FrameServiceStatus::OK);
    CHECK(frame.metadata.seq == seq);
    CHECK(frame.metadata.capture_ts_ns == 99);
    CHECK(frame.metadata.width == 2);
    CHECK(frame.metadata.height == 1);
    CHECK(frame.metadata.pixel_format == "uyvy");
    CHECK(frame.data == data);

    server.stop();
}

TEST_CASE("FrameServiceServer crops raw frame when crop_black is true") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = {
        128, 16, 128, 16,
        128, 235, 128, 235,
        128, 235, 128, 235,
        128, 16, 128, 16,
    };
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(8, 1, "uyvy", 99), data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    int fd = connect_raw_client(socket_path.path);
    REQUIRE(aiden::write_frame_message(
                fd,
                "{\"type\":\"request\",\"method\":\"latest_frame\",\"since_seq\":\"0\",\"timeout_ms\":0,\"format\":\"raw\",\"crop_black\":true}",
                std::vector<uint8_t>()) == FrameServiceStatus::OK);
    std::vector<uint8_t> cropped;
    const std::string header = read_response(fd, &cropped);

    CHECK(header.find("\"width\":4") != std::string::npos);
    CHECK(header.find("\"source_width\":8") != std::string::npos);
    CHECK(header.find("\"crop_x\":2") != std::string::npos);
    CHECK(header.find("\"crop_width\":4") != std::string::npos);
    CHECK(cropped == std::vector<uint8_t>{128, 235, 128, 235, 128, 235, 128, 235});

    ::close(fd);
    server.stop();
}

TEST_CASE("FrameServiceServer returns NO_NEW_FRAME for nonblocking latest request") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = payload({7});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 1), data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    CHECK(client.latest_frame(seq, 0, &frame) == FrameServiceStatus::NO_NEW_FRAME);

    server.stop();
}

TEST_CASE("FrameServiceServer long-polls until a newer frame arrives") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    FrameServiceStatus status = FrameServiceStatus::INTERNAL_ERROR;

    std::thread waiter([&]() {
        status = client.latest_frame(0, 1000, &frame);
    });

    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    std::vector<uint8_t> data = payload({9, 8});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 123), data.data(), data.size(), &seq) == FrameServiceStatus::OK);
    waiter.join();

    REQUIRE(status == FrameServiceStatus::OK);
    CHECK(frame.metadata.seq == seq);
    CHECK(frame.data == data);

    server.stop();
}

TEST_CASE("FrameServiceServer stop releases active long-poll clients before destruction") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    FrameServiceStatus status = FrameServiceStatus::INTERNAL_ERROR;

    std::thread waiter([&]() {
        status = client.latest_frame(0, 5000, &frame);
    });

    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    server.stop();
    waiter.join();
    bool released = status == FrameServiceStatus::TIMEOUT || status == FrameServiceStatus::TRANSPORT_ERROR;
    CHECK(released);
}

TEST_CASE("FrameServiceServer marks cached frames stale while recovering") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = payload({1, 2});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 10), data.data(), data.size(), &seq) == FrameServiceStatus::OK);
    server.set_state("RECOVERING");

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(client.latest_frame(0, 0, &frame) == FrameServiceStatus::OK);
    CHECK(frame.metadata.stale);
    server.stop();
}

TEST_CASE("FrameServiceServer returns SERVICE_RECOVERING without cached frame") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    server.set_state("RECOVERING");

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    CHECK(client.latest_frame(0, 0, &frame) == FrameServiceStatus::SERVICE_RECOVERING);
    server.stop();
}

TEST_CASE("FrameServiceServer wakes long-poll clients when entering recovery") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data = payload({1, 2});
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 10), data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    FrameServiceStatus status = FrameServiceStatus::INTERNAL_ERROR;
    std::thread waiter([&]() {
        status = client.latest_frame(seq, 5000, &frame);
    });

    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    server.set_state("RECOVERING");
    waiter.join();

    REQUIRE(status == FrameServiceStatus::OK);
    CHECK(frame.metadata.seq == seq);
    CHECK(frame.metadata.stale);
    server.stop();
}

TEST_CASE("FrameServiceServer restart request invokes registered handler") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    bool restarted = false;
    server.set_restart_handler([&]() { restarted = true; });
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    CHECK(client.restart() == FrameServiceStatus::OK);
    CHECK(restarted);
    server.stop();
}

TEST_CASE("FrameServiceServer supports get_frame and list_frames") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 2);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> one = payload({1});
    std::vector<uint8_t> two = payload({2});
    uint64_t seq1 = 0;
    uint64_t seq2 = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 10), one.data(), one.size(), &seq1) == FrameServiceStatus::OK);
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 20), two.data(), two.size(), &seq2) == FrameServiceStatus::OK);

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(client.get_frame(seq1, &frame) == FrameServiceStatus::OK);
    CHECK(frame.data == one);

    aiden::FrameListResult list;
    REQUIRE(client.list_frames(2, &list) == FrameServiceStatus::OK);
    REQUIRE(list.frames.size() == 2);
    CHECK(list.frames[0].seq == seq1);
    CHECK(list.frames[1].seq == seq2);

    server.stop();
}

TEST_CASE("FrameServiceServer limits concurrent large payload sends without blocking health") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);
    std::vector<uint8_t> data(16 * 1024 * 1024, 7);
    uint64_t seq = 0;
    REQUIRE(server.append_frame(metadata(1, 1, "uyvy", 123), data.data(), data.size(), &seq) == FrameServiceStatus::OK);

    int slow_fd = connect_raw_client(socket_path.path);
    request_latest_frame_raw(slow_fd);
    aiden::FrameWirePrefix prefix{};
    read_response_header(slow_fd, &prefix);
    REQUIRE(prefix.payload_len == data.size());

    int waiting_fd = connect_raw_client(socket_path.path);
    request_latest_frame_raw(waiting_fd);
    CHECK_FALSE(fd_readable_within(waiting_fd, 100));

    FrameServiceClient client(socket_path.path.c_str());
    HealthResult health;
    CHECK(client.health(&health) == FrameServiceStatus::OK);

    ::close(slow_fd);
    read_response_header(waiting_fd, &prefix);
    CHECK(prefix.payload_len == data.size());
    ::close(waiting_fd);

    server.stop();
}
