.PHONY: all configure build clean test test-agent-webui test-clean sandbox-start sandbox-logs sandbox-stop

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
	cmake --build $(TEST_BUILD_DIR) --parallel $$(getconf _NPROCESSORS_ONLN)

test: test-build
	cd $(TEST_BUILD_DIR) && ctest --output-on-failure

test-agent-webui:
	node src/agent/internal/agent/web_ui_test/history_reconciliation.test.js

test-clean:
	rm -rf $(TEST_BUILD_DIR)

clean:
	rm -rf $(BUILD_DIR)

sandbox-start:
	./scripts/start_docker_sandbox.sh

sandbox-logs:
	docker compose logs -f aiden

sandbox-stop:
	docker compose down
