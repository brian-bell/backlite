FROM node:24-slim

ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    openssh-client \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*

# Claude Code CLI only (this image is claude_code-only by design)
RUN npm install -g @anthropic-ai/claude-code

# Skill bundles baked at /opt/backflow/skills/{code,review,read}/
COPY skills/ /opt/backflow/skills/

# Agent user
RUN useradd -m -s /bin/bash agent
USER agent
WORKDIR /home/agent

RUN git config --global user.name "backlite-agent" \
    && git config --global user.email "backlite-agent@noreply.github.com" \
    && git config --global init.defaultBranch main

COPY --chown=agent:agent entrypoint.sh /home/agent/entrypoint.sh
RUN chmod +x /home/agent/entrypoint.sh

ENTRYPOINT ["/home/agent/entrypoint.sh"]
