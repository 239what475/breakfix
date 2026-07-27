ARG BREAKFIX_BASE_IMAGE=breakfix-k8s-base:latest
FROM ${BREAKFIX_BASE_IMAGE}

COPY challenge.yaml /breakfix/challenge.yaml
COPY problem.md /problem.md
COPY generate.sh /breakfix/generate.sh
COPY answer.sh /answer.sh
COPY checks/ /checks/
COPY hints/ /hints/
RUN chmod +x /breakfix/generate.sh /answer.sh /checks/checkpoints.sh
ENTRYPOINT ["/breakfix/runtime-init.sh"]
CMD ["sleep", "infinity"]
