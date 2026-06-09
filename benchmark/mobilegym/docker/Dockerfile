# Aiden MobileGym Docker Setup

# ============ MobileGym Simulator ============
FROM node:18-bullseye AS mobilegym-base

# 支持构建时代理
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY}

WORKDIR /mobilegym

# Install system dependencies for Playwright + git
RUN apt-get update && apt-get install -y \
    git \
    python3 \
    python3-pip \
    python3-venv \
    libnss3 \
    libnspr4 \
    libatk1.0-0 \
    libatk-bridge2.0-0 \
    libcups2 \
    libdrm2 \
    libdbus-1-3 \
    libxkbcommon0 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    && rm -rf /var/lib/apt/lists/*

# Clone MobileGym (直接从上游拉，避免依赖 submodule)
ARG MOBILEGYM_REPO=https://github.com/Purewhiter/mobilegym.git
ARG MOBILEGYM_BRANCH=main
RUN git clone --depth 1 --branch ${MOBILEGYM_BRANCH} ${MOBILEGYM_REPO} /tmp/mobilegym && \
    mv /tmp/mobilegym/* /tmp/mobilegym/.* /mobilegym/ 2>/dev/null || true && \
    rm -rf /tmp/mobilegym

# Install Python dependencies
RUN cd /mobilegym && \
    python3 -m venv /opt/venv && \
    . /opt/venv/bin/activate && \
    pip install -r bench_env/requirements.txt

# Install Node.js dependencies and build
RUN npm install && npm run build

# Install Playwright browsers
RUN . /opt/venv/bin/activate && playwright install chromium

ENV PATH="/opt/venv/bin:$PATH"
EXPOSE 4173

CMD ["npm", "run", "preview", "--", "--host", "0.0.0.0", "--port", "4173"]


# ============ Go Agent Daemon ============
FROM golang:1.23-bullseye AS daemon-builder

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY} \
    GOPROXY=https://goproxy.io,direct \
    GOTOOLCHAIN=auto

WORKDIR /build

# Copy go.mod and go.sum for caching
COPY src/agent/go.mod src/agent/go.sum ./
RUN go mod download

# Copy source code
COPY src/agent/ ./

# Build daemon
RUN CGO_ENABLED=0 GOOS=linux go build -o daemon ./cmd/daemon


FROM debian:bullseye-slim AS daemon-runtime

# daemon 是静态二进制，不需要安装额外包
# RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

COPY --from=daemon-builder /build/daemon /usr/local/bin/daemon

WORKDIR /app
EXPOSE 8080

CMD ["daemon", "-config", "/config", "-addr", "0.0.0.0:8080"]


# ============ Test Runner ============
FROM python:3.11-slim AS test-runner

ARG HTTP_PROXY
ARG HTTPS_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY}

WORKDIR /app

# Install uv (先升级 pip)
RUN pip install --upgrade pip && pip install uv

# Copy benchmark requirements
COPY benchmark/pyproject.toml benchmark/uv.lock /app/benchmark/

# Install dependencies
RUN cd /app/benchmark && uv sync

# Copy test code and adapter
COPY benchmark/mobilegym /app/benchmark/mobilegym
COPY benchmark/tests /app/benchmark/tests

ENV PATH="/app/benchmark/.venv/bin:$PATH"
WORKDIR /app/benchmark

ENTRYPOINT ["uv", "run", "python", "mobilegym/scripts/run_aiden.py"]
