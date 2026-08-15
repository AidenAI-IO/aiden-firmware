# Aiden MobileGym Docker Setup

# ============ MobileGym Simulator ============
FROM node:22-bookworm AS mobilegym-base

# 支持构建时代理
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG PLAYWRIGHT_DOWNLOAD_HOST
ARG PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY} \
    PLAYWRIGHT_DOWNLOAD_HOST=${PLAYWRIGHT_DOWNLOAD_HOST} \
    PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST=${PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST} \
    PUPPETEER_SKIP_DOWNLOAD=true

WORKDIR /mobilegym

# Install system dependencies for Playwright + git + CJK fonts
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
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    fonts-noto-core \
    libcairo2 \
    libasound2 \
    && rm -rf /var/lib/apt/lists/*

# Clone the exact MobileGym commit pinned by the firmware submodule.
ARG MOBILEGYM_REPO=https://github.com/AidenAI-IO/mobilegym.git
ARG MOBILEGYM_COMMIT
RUN test -n "${MOBILEGYM_COMMIT}" && \
    git init /tmp/mobilegym && \
    git -C /tmp/mobilegym remote add origin "${MOBILEGYM_REPO}" && \
    git -C /tmp/mobilegym \
        -c http.version=HTTP/1.1 \
        -c http.lowSpeedLimit=1024 \
        -c http.lowSpeedTime=60 \
        fetch --depth 1 --progress origin "${MOBILEGYM_COMMIT}" && \
    git -C /tmp/mobilegym checkout --detach FETCH_HEAD && \
    test "$(git -C /tmp/mobilegym rev-parse HEAD)" = "${MOBILEGYM_COMMIT}" && \
    cp -a /tmp/mobilegym/. /mobilegym/ && \
    rm -rf /tmp/mobilegym
LABEL io.aiden.mobilegym.commit="${MOBILEGYM_COMMIT}"

# Install Python dependencies
RUN cd /mobilegym && \
    python3 -m venv /opt/venv && \
    . /opt/venv/bin/activate && \
    pip install -r bench_env/requirements.txt requests

# Install Node.js dependencies and build
RUN npm install && npm run build

# Install Playwright browsers
RUN . /opt/venv/bin/activate && playwright install chromium

ENV PATH="/opt/venv/bin:$PATH"
EXPOSE 4173

CMD ["npm", "run", "preview", "--", "--host", "0.0.0.0", "--port", "4173"]
