# syntax=docker/dockerfile:1

FROM golang:1.26.3-bookworm AS build
WORKDIR /workspace
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/breakfix-agent-worker ./cmd/agent-worker

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/breakfix-agent-worker /usr/local/bin/breakfix-agent-worker
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/breakfix-agent-worker"]
