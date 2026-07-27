# Verifier image: BuildKit plus the deterministic Kubernetes verification
# binary. It intentionally contains neither Node.js nor a model/agent CLI.
FROM moby/buildkit:v0.26.2@sha256:de10faf919fc71ba4eb1dd7bd6449566d012b0c9436b1c61bfee21d621b009aa

COPY breakfix-verifier /usr/local/bin/verifier

RUN mkdir -p /workspace /var/lib/buildkit
WORKDIR /workspace

ENV BUILDER=buildkit
ENTRYPOINT ["/usr/local/bin/verifier"]
