.PHONY: all build clean

all: build

build:
	$(MAKE) -C workspace

clean:
	$(MAKE) -C workspace clean
