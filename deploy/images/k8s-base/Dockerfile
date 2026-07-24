FROM ubuntu:22.04

ARG KUBECTL_VERSION=v1.36.2

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
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /tmp/kubectl; \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256" -o /tmp/kubectl.sha256; \
    echo "$(cat /tmp/kubectl.sha256)  /tmp/kubectl" | sha256sum -c -; \
    install -m 0755 /tmp/kubectl /usr/local/bin/kubectl; \
    rm -f /tmp/kubectl /tmp/kubectl.sha256

COPY runtime-init.sh /breakfix/runtime-init.sh
RUN chmod +x /breakfix/runtime-init.sh && mkdir -p /breakfix /var/lib/breakfix /root/.kube

SHELL ["/bin/bash", "-c"]
CMD ["sleep", "infinity"]
