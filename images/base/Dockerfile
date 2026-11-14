# Default agent image. M5 adds the harness, runtimes and a headless browser.
FROM debian:bookworm-slim

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bash ca-certificates curl git less nano openssh-client procps sudo tini unzip vim-tiny wget \
 && rm -rf /var/lib/apt/lists/*

# Terminals and harnesses run as "agent"; sandboxd (the entrypoint) runs as root
# and drops to this account. The agent may use sudo inside its own sandbox.
RUN useradd --create-home --home-dir /home/agent --shell /bin/bash --uid 1000 agent \
 && echo 'agent ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent

ENV LANG=C.UTF-8
WORKDIR /home/agent
