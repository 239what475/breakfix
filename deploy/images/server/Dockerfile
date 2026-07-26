# syntax=docker/dockerfile:1

FROM node:24-bookworm-slim AS frontend
WORKDIR /workspace
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN npm ci --prefix frontend
COPY frontend ./frontend
COPY cmd/server ./cmd/server
RUN npm run build --prefix frontend

FROM golang:1.26.3-bookworm AS build
WORKDIR /workspace
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /workspace/cmd/server/frontend/dist ./cmd/server/frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/breakfix-server ./cmd/server

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/breakfix-server /usr/local/bin/breakfix-server
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/breakfix-server"]
