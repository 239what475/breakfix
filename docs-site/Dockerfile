FROM nginxinc/nginx-unprivileged:1.27-alpine

ARG SOURCE
ARG VERSION
ARG REVISION

LABEL org.opencontainers.image.source="$SOURCE" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION"

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY public/ /usr/share/nginx/html/

EXPOSE 8080
