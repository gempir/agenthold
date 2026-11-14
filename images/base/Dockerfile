# syntax=docker/dockerfile:1
# Default agent image: Claude Code (through its ACP adapter), git, common
# runtimes, build tools and a headless browser. Agents run as "agent".
FROM debian:trixie-slim

ARG NODE_MAJOR=24
ARG CLAUDE_AGENT_ACP_VERSION=0.107.0

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bash ca-certificates curl wget git git-lfs openssh-client gnupg less nano vim-tiny \
      procps psmisc htop iproute2 iputils-ping dnsutils netcat-openbsd \
      sudo tini unzip zip xz-utils jq ripgrep fd-find tree file make build-essential pkg-config \
      python3 python3-pip python3-venv pipx \
      chromium fonts-liberation fonts-noto-color-emoji \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && rm -rf /var/lib/apt/lists/*

# Node.js from NodeSource (Debian's is too old for current harnesses).
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit @agentclientprotocol/claude-agent-acp@${CLAUDE_AGENT_ACP_VERSION} \
 && npm cache clean --force

# Headless browser for agents (Playwright and Puppeteer can use it).
ENV CHROME_BIN=/usr/bin/chromium \
    PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium \
    PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium \
    PUPPETEER_SKIP_DOWNLOAD=1

RUN useradd --create-home --home-dir /home/agent --shell /bin/bash --uid 1000 agent \
 && echo 'agent ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent \
 && mkdir -p /home/agent/.local/bin && chown -R agent:agent /home/agent

ENV LANG=C.UTF-8 \
    PATH=/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORKDIR /home/agent
