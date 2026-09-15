# syntax=docker/dockerfile:1

FROM gcr.io/distroless/static-debian12:nonroot
COPY document-agent-fixture /usr/local/bin/document-agent-fixture
ENTRYPOINT ["/usr/local/bin/document-agent-fixture"]
