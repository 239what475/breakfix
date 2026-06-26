FROM alpine:3.20

RUN apk add --no-cache \
    bash \
    curl \
    vim \
    grep \
    gawk \
    sed \
    findutils \
    tar \
    gzip \
    coreutils \
    procps \
    htop \
    net-tools \
    iputils \
    bind-tools \
    tcpdump \
    python3 \
    && ln -sf /usr/bin/python3 /usr/bin/python

# Default shell
SHELL ["/bin/bash", "-c"]
CMD ["sleep", "infinity"]
