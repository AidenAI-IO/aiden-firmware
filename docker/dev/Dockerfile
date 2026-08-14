FROM golang:1.23-bookworm AS agent-builder

ENV GOTOOLCHAIN=auto
WORKDIR /build

COPY src/agent/go.mod src/agent/go.sum ./
RUN go mod download
COPY src/agent/ ./
RUN mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agent ./cmd/daemon

FROM debian:bookworm AS config-web-builder

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc g++ \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /build

COPY src/ ./src/
RUN mkdir -p /out \
    && gcc -std=c11 -O2 -Isrc -c src/cJSON/cJSON.c -o cjson.o \
    && g++ -std=c++11 -O2 -Isrc \
        src/aiden_log.cpp \
        src/config_web.cpp \
        src/config_web_static_assets.cpp \
        src/agent_toml.cpp \
        src/frame_service_protocol.cpp \
        src/service_status.cpp \
        src/uds_client.cpp \
        src/uds_message.cpp \
        src/system_env_parser.cpp \
        src/wifi_config.cpp \
        cjson.o -pthread -o /out/config_web

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl util-linux \
    && rm -rf /var/lib/apt/lists/*

COPY --from=agent-builder /out/agent /oem/usr/bin/agent
COPY --from=config-web-builder /out/config_web /usr/local/bin/config_web
COPY src/config_web/web/ /oem/usr/share/aiden/config-web/
COPY src/agent/config/skills/ /oem/usr/share/aiden/skills/
COPY docker/dev/agent.toml /opt/aiden/defaults/agent.toml
COPY docker/dev/agent-service.sh /usr/local/bin/aiden-agent-service
COPY docker/dev/entrypoint.sh /usr/local/bin/aiden-docker-entrypoint

RUN chmod 0755 \
        /oem/usr/bin/agent \
        /usr/local/bin/config_web \
        /usr/local/bin/aiden-agent-service \
        /usr/local/bin/aiden-docker-entrypoint

ENV AIDEN_AGENT_BIN=/oem/usr/bin/agent \
    AIDEN_AGENT_INIT_SCRIPT=/usr/local/bin/aiden-agent-service \
    AIDEN_BUNDLED_SKILLS_DIR=/oem/usr/share/aiden/skills \
    AIDEN_SYSTEM_ENV=/userdata/system/env

EXPOSE 80 8080
VOLUME ["/userdata"]

ENTRYPOINT ["/usr/local/bin/aiden-docker-entrypoint"]
