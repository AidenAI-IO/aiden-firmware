FROM debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258

ARG DEBIAN_FRONTEND=noninteractive

COPY scripts/debian-stage3/debian.sources /etc/apt/sources.list.d/debian.sources

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        acl \
        attr \
        binfmt-support \
        binutils \
        binutils-arm-linux-gnueabihf \
        ca-certificates \
        debootstrap \
        device-tree-compiler \
        e2fsprogs \
        file \
        git \
        jq \
        libcap2-bin \
        openssl \
        python3-minimal \
        qemu-user-static \
        ripgrep \
        rsync \
        shellcheck \
        systemd \
        xz-utils \
        zstd \
    && rm -rf /var/lib/apt/lists/*
