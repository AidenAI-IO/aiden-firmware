CROSS_COMPILE := ../pico-sdk/tools/linux/toolchain/arm-rockchip830-linux-uclibcgnueabihf/bin/arm-rockchip830-linux-uclibcgnueabihf-
CC := $(CROSS_COMPILE)gcc
CXX := $(CROSS_COMPILE)g++
AR := $(CROSS_COMPILE)ar

SDK_PATH := ../pico-sdk
ROCKIT_PATH := $(SDK_PATH)/media/rockit/rockit
MPP_PATH := $(SDK_PATH)/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf
RGA_PATH := $(SDK_PATH)/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf
IVE_PATH := $(SDK_PATH)/media/ive/ive

CFLAGS := -Wall -O2
CXXFLAGS := -Wall -O2 -std=c++11 -I$(ROCKIT_PATH)/mpi/sdk/include
LDFLAGS := -L$(ROCKIT_PATH)/lib/lib32 -L$(MPP_PATH)/lib -L$(RGA_PATH)/lib -L$(IVE_PATH)/lib -lrockit -lrockchip_mpp -lrga -lrve -lpthread -lstdc++ -lm

OUTPUT := ../output

all: hello trigger libaiden.a examples

hello: hello.c
	$(CC) $(CFLAGS) $^ -o $(OUTPUT)/$@

trigger: trigger.c
	$(CC) $(CFLAGS) $^ -o $(OUTPUT)/$@

# SDK library
libaiden.a: aiden_sdk.o
	$(AR) rcs $@ $^

aiden_sdk.o: aiden_sdk.cpp aiden_sdk.h
	$(CXX) $(CXXFLAGS) -c $< -o $@

# Examples
examples: example_wakeup example_audio_capture example_audio_play example_camera_capture

example_wakeup: example_wakeup.cpp libaiden.a
	$(CXX) $(CXXFLAGS) $< -L. -laiden $(LDFLAGS) -o $(OUTPUT)/$@

example_audio_capture: example_audio_capture.cpp libaiden.a
	$(CXX) $(CXXFLAGS) $< -L. -laiden $(LDFLAGS) -o $(OUTPUT)/$@

example_audio_play: example_audio_play.cpp libaiden.a
	$(CXX) $(CXXFLAGS) $< -L. -laiden $(LDFLAGS) -o $(OUTPUT)/$@

example_camera_capture: example_camera_capture.cpp libaiden.a
	$(CXX) $(CXXFLAGS) $< -L. -laiden $(LDFLAGS) -o $(OUTPUT)/$@

clean:
	rm -f *.o libaiden.a
	rm -f $(OUTPUT)/hello $(OUTPUT)/trigger
	rm -f $(OUTPUT)/example_wakeup $(OUTPUT)/example_audio_capture $(OUTPUT)/example_audio_play $(OUTPUT)/example_camera_capture

.PHONY: all clean examples
