.PHONY: all configure build clean test test-clean

BUILD_DIR := build
TEST_BUILD_DIR := build-host

all: build

configure:
	cmake -S . -B $(BUILD_DIR)

build: configure
	cmake --build $(BUILD_DIR)

test-configure:
	cmake -S . -B $(TEST_BUILD_DIR) -DAIDEN_TESTS=ON

test-build: test-configure
	cmake --build $(TEST_BUILD_DIR)

test: test-build
	$(TEST_BUILD_DIR)/tests/aiden_tests

test-clean:
	rm -rf $(TEST_BUILD_DIR)

clean:
	rm -rf $(BUILD_DIR)
