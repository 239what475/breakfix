FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    curl \
    vim \
    tmux \
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
    iputils-ping \
    dnsutils \
    tcpdump \
    python3 \
    && rm -rf /var/lib/apt/lists/*

SHELL ["/bin/bash", "-c"]
CMD ["sleep", "infinity"]
