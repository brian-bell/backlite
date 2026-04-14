FROM node:24-slim

ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI and Codex CLI
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Create reader user
RUN useradd -m -s /bin/bash agent
USER agent
WORKDIR /home/agent

COPY --chown=agent:agent reader-entrypoint.sh status_writer.sh reader_helpers.sh \
    read-embed.sh read-lookup.sh read-similar.sh \
    /home/agent/
RUN chmod +x /home/agent/reader-entrypoint.sh \
    /home/agent/read-embed.sh /home/agent/read-lookup.sh /home/agent/read-similar.sh

ENTRYPOINT ["/home/agent/reader-entrypoint.sh"]
