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

# Pre-fetch + extraction deps. Lives in its own dir so the agent's npm state
# is not mingled with the project root. npm ci pins the transitive tree to
# the committed lockfile so two builds a month apart don't drift.
COPY --chown=agent:agent extractor/package.json extractor/package-lock.json /home/agent/extractor/
RUN cd /home/agent/extractor && npm ci --no-audit --no-fund --omit=dev
COPY --chown=agent:agent extractor/extract.js /home/agent/extractor/

COPY --chown=agent:agent reader-entrypoint.sh status_writer.sh reader_helpers.sh \
    read-embed.sh read-lookup.sh read-similar.sh fetch-and-extract.sh \
    /home/agent/
RUN chmod +x /home/agent/reader-entrypoint.sh \
    /home/agent/read-embed.sh /home/agent/read-lookup.sh /home/agent/read-similar.sh \
    /home/agent/fetch-and-extract.sh

ENTRYPOINT ["/home/agent/reader-entrypoint.sh"]
