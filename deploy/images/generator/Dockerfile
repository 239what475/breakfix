# Generator image — runs the agent workflow as a K8s Job
FROM node:24

# BuildKit daemon (for building challenge images)
COPY --from=moby/buildkit:master /usr/bin/buildkitd /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildctl /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildkit-runc /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildkit-cni-* /usr/local/bin/

# Claude Code CLI (for LLM agent)
RUN npm config set registry https://registry.npmmirror.com/ && \
    npm install -g @anthropic-ai/claude-code@latest

RUN cat >/usr/local/bin/breakfix-claude <<'EOF' && chmod +x /usr/local/bin/breakfix-claude
#!/bin/sh
set -eu
# runuser waits for a child process. exec.CommandContext can then kill only
# runuser while Claude keeps stdout open, leaving the caller blocked in its
# output reader. setpriv execs the CLI in place after dropping privileges.
exec env HOME=/home/node USER=node LOGNAME=node \
    setpriv --reuid=node --regid=node --init-groups /usr/local/bin/claude "$@"
EOF

# Generator binary
COPY bin/generator /usr/local/bin/generator

# Workspace for challenge generation
RUN mkdir -p /workspace /var/lib/buildkit
WORKDIR /workspace

ENV BUILDER=buildkit
ENTRYPOINT ["/usr/local/bin/generator"]
