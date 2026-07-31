# Builder only adds a challenge bundle to trusted platform base artifacts. It
# It has no shell or candidate-code execution path.
FROM gcr.io/distroless/static-debian12:nonroot

COPY breakfix-builder /usr/local/bin/builder

ENTRYPOINT ["/usr/local/bin/builder"]
