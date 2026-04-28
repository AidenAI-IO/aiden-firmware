.PHONY: all build clean

all: build

build:
	$(MAKE) -C src

clean:
	$(MAKE) -C src clean
