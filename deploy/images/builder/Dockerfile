# Rootless BuildKit executes untrusted Dockerfile instructions. The runtime
# image contains only BuildKit and the statically linked handoff binary.
FROM moby/buildkit:v0.26.2-rootless@sha256:0ffa2fcf6b8757c47d569b3ef0f03f9d5eb3b9ff5ce68d858f994f89b749da0c

COPY breakfix-builder /usr/local/bin/builder

WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/builder"]
