CROSS_COMPILE := ../pico-sdk/tools/linux/toolchain/arm-rockchip830-linux-uclibcgnueabihf/bin/arm-rockchip830-linux-uclibcgnueabihf-
CC := $(CROSS_COMPILE)gcc
CXX := $(CROSS_COMPILE)g++
AR := $(CROSS_COMPILE)ar

SDK_PATH := ../pico-sdk
ROCKIT_PATH := $(SDK_PATH)/media/rockit/rockit
MPP_PATH := $(SDK_PATH)/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf
RGA_PATH := $(SDK_PATH)/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf
IVE_PATH := $(SDK_PATH)/media/ive/ive

BUILD_DIR ?= ../build
OBJ_DIR := $(BUILD_DIR)/obj
LIB_DIR := $(BUILD_DIR)/lib
BIN_DIR := $(BUILD_DIR)/bin

CFLAGS := -Wall -O2
CXXFLAGS := -Wall -O2 -std=c++11 -I$(ROCKIT_PATH)/mpi/sdk/include
LDFLAGS := -L$(ROCKIT_PATH)/lib/lib32 -L$(MPP_PATH)/lib -L$(RGA_PATH)/lib -L$(IVE_PATH)/lib -Wl,-rpath,/oem/usr/lib -lrockit -lrockchip_mpp -lrga -lrve -lpthread -lstdc++ -lm

SDK_LIBRARY := $(LIB_DIR)/libaiden.a
SDK_OBJECT := $(OBJ_DIR)/aiden_sdk.o
C_BINARIES := $(BIN_DIR)/hello $(BIN_DIR)/trigger
EXAMPLE_BINARIES := \
	$(BIN_DIR)/example_wakeup \
	$(BIN_DIR)/example_audio_capture \
	$(BIN_DIR)/example_audio_play \
	$(BIN_DIR)/example_camera_capture

all: directories $(C_BINARIES) $(SDK_LIBRARY) examples

examples: directories $(EXAMPLE_BINARIES)

directories:
	mkdir -p $(OBJ_DIR) $(LIB_DIR) $(BIN_DIR)

$(BIN_DIR)/hello: hello.c | directories
	$(CC) $(CFLAGS) $< -o $@

$(BIN_DIR)/trigger: trigger.c | directories
	$(CC) $(CFLAGS) $< -o $@

$(SDK_LIBRARY): $(SDK_OBJECT) | directories
	rm -f $@
	$(AR) rcs $@ $^

$(OBJ_DIR)/aiden_sdk.o: aiden_sdk.cpp aiden_sdk.h | directories
	$(CXX) $(CXXFLAGS) -c $< -o $@

$(BIN_DIR)/example_wakeup: example_wakeup.cpp $(SDK_LIBRARY) | directories
	$(CXX) $(CXXFLAGS) $< -L$(LIB_DIR) -laiden $(LDFLAGS) -o $@

$(BIN_DIR)/example_audio_capture: example_audio_capture.cpp $(SDK_LIBRARY) | directories
	$(CXX) $(CXXFLAGS) $< -L$(LIB_DIR) -laiden $(LDFLAGS) -o $@

$(BIN_DIR)/example_audio_play: example_audio_play.cpp $(SDK_LIBRARY) | directories
	$(CXX) $(CXXFLAGS) $< -L$(LIB_DIR) -laiden $(LDFLAGS) -o $@

$(BIN_DIR)/example_camera_capture: example_camera_capture.cpp $(SDK_LIBRARY) | directories
	$(CXX) $(CXXFLAGS) $< -L$(LIB_DIR) -laiden $(LDFLAGS) -o $@

clean:
	rm -rf $(BUILD_DIR)
	rm -f aiden_sdk.o libaiden.a
	rm -f ../output/hello ../output/trigger
	rm -f ../output/example_wakeup ../output/example_audio_capture ../output/example_audio_play ../output/example_camera_capture

.PHONY: all clean directories examples
