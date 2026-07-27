# syntax=docker/dockerfile:1

FROM curlimages/curl:8.12.1 AS vcluster
ARG TARGETARCH
ARG VCLUSTER_VERSION=v0.35.1
RUN case "${TARGETARCH}" in \
      amd64) checksum="baf9effb1de7c17cfa4462aacf92d913b4ec4e359c6e711e33c43f7e5e5b0dab" ;; \
      arm64) checksum="1468cc7ed5ff31313332a558c3ca907eee1c65d6b0cbcd6dd16fdd29b342c031" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/loft-sh/vcluster/releases/download/${VCLUSTER_VERSION}/vcluster-linux-${TARGETARCH}" -o /tmp/vcluster && \
    echo "${checksum}  /tmp/vcluster" | sha256sum -c - && \
    chmod 0755 /tmp/vcluster

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=vcluster /tmp/vcluster /usr/local/bin/vcluster
COPY breakfix-controller /usr/local/bin/breakfix-controller
ENTRYPOINT ["/usr/local/bin/breakfix-controller"]
