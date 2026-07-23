ARG BREAKFIX_BASE_IMAGE=breakfix-k8s-base:latest
FROM ${BREAKFIX_BASE_IMAGE}

COPY challenge.yaml /breakfix/challenge.yaml
COPY problem.md /home/user/problem.md
COPY generate.sh /breakfix/generate.sh
COPY checks /checks
COPY answer.sh /answer.sh
RUN chmod +x /breakfix/generate.sh /checks/checkpoints.sh /answer.sh
ENTRYPOINT ["/breakfix/runtime-init.sh"]
CMD ["sleep", "infinity"]
