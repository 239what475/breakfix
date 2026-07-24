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

COPY runtime-init.sh /breakfix/runtime-init.sh
RUN chmod +x /breakfix/runtime-init.sh && mkdir -p /breakfix /var/lib/breakfix

SHELL ["/bin/bash", "-c"]
CMD ["sleep", "infinity"]
