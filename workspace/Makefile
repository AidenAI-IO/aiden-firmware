CC := ../sdk/tools/linux/toolchain/arm-rockchip830-linux-uclibcgnueabihf/bin/arm-rockchip830-linux-uclibcgnueabihf-gcc
CFLAGS := -Wall -O2

all: hello trigger

hello: hello.c
	$(CC) $(CFLAGS) $^ -o ../output/$@

trigger: trigger.c
	$(CC) $(CFLAGS) $^ -o ../output/$@


clean:
	rm -f hello
