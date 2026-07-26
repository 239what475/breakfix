# syntax=docker/dockerfile:1

FROM golang:1.26.3-bookworm AS build
WORKDIR /workspace
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/breakfix-controller ./cmd/controller

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

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=vcluster /tmp/vcluster /usr/local/bin/vcluster
COPY --from=build /out/breakfix-controller /usr/local/bin/breakfix-controller
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/breakfix-controller"]
