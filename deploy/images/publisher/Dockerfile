# Publisher does not execute challenge files. It only validates and uploads a
# Server-provided OCI archive using its task-scoped target image name.
FROM gcr.io/distroless/static-debian12:nonroot

COPY breakfix-publisher /usr/local/bin/publisher

ENTRYPOINT ["/usr/local/bin/publisher"]
