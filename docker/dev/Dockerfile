FROM golang:1.23-bookworm AS agent-builder

ENV GOTOOLCHAIN=auto
WORKDIR /build

COPY src/agent/go.mod src/agent/go.sum ./
RUN go mod download
COPY src/agent/ ./
RUN mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agent ./cmd/daemon

FROM node:16-bookworm AS wetty-builder

RUN npm install --global --omit=optional wetty@2.5.0 sass@1.69.7 \
    && npm cache clean --force

FROM debian:bookworm-slim AS runtime-base

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        python3 \
        python3-pip \
        util-linux \
    && rm -rf /var/lib/apt/lists/*

FROM runtime-base AS runtime-tools-builder

ARG TARGETARCH

RUN set -eux; \
    architecture="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$architecture" in \
        amd64) \
            fq_sha256="41c277c59dffacfba9c9f9f4ad3a75bef591d341e40b9ac89fc85acbdf645fee"; \
            yq_sha256="fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4"; \
            rg_target="x86_64-unknown-linux-musl"; \
            rg_sha256="33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c"; \
            ;; \
        arm64) \
            fq_sha256="217eba0d2cd03c8cbf9c54e45ecb9700b7100d592b71410ee3628c6d423cd328"; \
            yq_sha256="578648e463a11c1b6db6010cbf41eafed6bee79466fcffa1bb446672cf7945ea"; \
            rg_target="aarch64-unknown-linux-musl"; \
            rg_sha256="800b1e7206afe799dfb5a6901f23147cfaabe0e52210538100f61e86e1740915"; \
            ;; \
        *) echo "Unsupported runtime tools architecture: $architecture" >&2; exit 1 ;; \
    esac; \
    mkdir -p /out; \
    curl -fsSL --retry 3 \
        "https://github.com/wader/fq/releases/download/v0.17.0/fq_0.17.0_linux_${architecture}.tar.gz" \
        -o /tmp/fq.tar.gz; \
    echo "$fq_sha256  /tmp/fq.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/fq.tar.gz -C /out fq; \
    curl -fsSL --retry 3 \
        "https://github.com/mikefarah/yq/releases/download/v4.53.3/yq_linux_${architecture}" \
        -o /out/yq; \
    echo "$yq_sha256  /out/yq" | sha256sum -c -; \
    curl -fsSL --retry 3 \
        "https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-${rg_target}.tar.gz" \
        -o /tmp/rg.tar.gz; \
    echo "$rg_sha256  /tmp/rg.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/rg.tar.gz -C /tmp; \
    cp "/tmp/ripgrep-15.2.0-${rg_target}/rg" /out/rg; \
    chmod 0755 /out/fq /out/rg /out/yq

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
        src/frame_service_protocol.cpp \
        src/service_status.cpp \
        src/uds_client.cpp \
        src/uds_message.cpp \
        src/system_env_parser.cpp \
        src/wifi_config.cpp \
        cjson.o -pthread -o /out/config_web

FROM runtime-base AS runtime

COPY --from=agent-builder /out/agent /oem/usr/bin/agent
COPY --from=config-web-builder /out/config_web /usr/local/bin/config_web
COPY --from=wetty-builder /usr/local/bin/node /usr/local/bin/node
COPY --from=wetty-builder /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=runtime-tools-builder /out/fq /usr/bin/fq
COPY --from=runtime-tools-builder /out/rg /usr/bin/rg
COPY --from=runtime-tools-builder /out/yq /usr/bin/yq
COPY src/config_web/web/ /oem/usr/share/aiden/config-web/
COPY src/agent/config/skills/ /oem/usr/share/aiden/skills/
COPY docker/dev/agent.toml /opt/aiden/defaults/agent.toml
COPY docker/dev/agent-service.sh /usr/local/bin/aiden-agent-service
COPY docker/dev/entrypoint.sh /usr/local/bin/aiden-docker-entrypoint
COPY docker/dev/wetty /usr/local/bin/wetty

RUN chmod 0755 \
        /oem/usr/bin/agent \
        /usr/local/bin/config_web \
        /usr/local/bin/aiden-agent-service \
        /usr/local/bin/aiden-docker-entrypoint \
        /usr/local/bin/wetty

ENV AIDEN_AGENT_BIN=/oem/usr/bin/agent \
    AIDEN_AGENT_INIT_SCRIPT=/usr/local/bin/aiden-agent-service \
    AIDEN_BUNDLED_SKILLS_DIR=/oem/usr/share/aiden/skills \
    AIDEN_SYSTEM_ENV=/userdata/system/env \
    PYTHONUSERBASE=/userdata/agent/python \
    PIP_USER=1 \
    PIP_BREAK_SYSTEM_PACKAGES=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

EXPOSE 80 8080
VOLUME ["/userdata"]

ENTRYPOINT ["/usr/local/bin/aiden-docker-entrypoint"]
