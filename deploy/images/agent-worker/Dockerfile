# syntax=docker/dockerfile:1

FROM gcr.io/distroless/static-debian12:nonroot
COPY breakfix-agent-worker /usr/local/bin/breakfix-agent-worker
ENTRYPOINT ["/usr/local/bin/breakfix-agent-worker"]
