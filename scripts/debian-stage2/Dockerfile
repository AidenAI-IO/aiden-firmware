FROM debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258

ARG DEBIAN_FRONTEND=noninteractive

COPY scripts/debian-stage2/debian.sources /etc/apt/sources.list.d/debian.sources

RUN dpkg --add-architecture armhf \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        binutils-arm-linux-gnueabihf \
        binutils \
        ca-certificates \
        cmake \
        curl \
        file \
        g++-arm-linux-gnueabihf \
        gcc-arm-linux-gnueabihf \
        git \
        ninja-build \
        patch \
    && rm -rf /var/lib/apt/lists/*
