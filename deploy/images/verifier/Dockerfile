# Verifier image: BuildKit plus the deterministic Kubernetes verification
# binary. It intentionally contains neither Node.js nor a model/agent CLI.
FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=moby/buildkit:master /usr/bin/buildkitd /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildctl /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildkit-runc /usr/local/bin/
COPY --from=moby/buildkit:master /usr/bin/buildkit-cni-* /usr/local/bin/

COPY bin/verifier /usr/local/bin/verifier

RUN mkdir -p /workspace /var/lib/buildkit
WORKDIR /workspace

ENV BUILDER=buildkit
ENTRYPOINT ["/usr/local/bin/verifier"]
