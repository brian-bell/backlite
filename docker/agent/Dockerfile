FROM node:24-slim

ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    build-essential \
    python3 \
    python3-pip \
    jq \
    openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Install Go
RUN GOARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
    && echo "==> Installing Go for ${GOARCH}" \
    && curl -fsSL "https://go.dev/dl/go1.24.1.linux-${GOARCH}.tar.gz" | tar -C /usr/local -xz \
    && /usr/local/go/bin/go version
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOFLAGS="-tags=nocontainers"

# Install GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI and Codex CLI
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Create agent user
RUN useradd -m -s /bin/bash agent
USER agent
WORKDIR /home/agent

# Configure git
RUN git config --global user.name "backlite-agent" \
    && git config --global user.email "backlite-agent@noreply.github.com" \
    && git config --global init.defaultBranch main

COPY --chown=agent:agent entrypoint.sh status_writer.sh /home/agent/
RUN chmod +x /home/agent/entrypoint.sh

ENTRYPOINT ["/home/agent/entrypoint.sh"]
