ARG BREAKFIX_BASE_IMAGE=breakfix-k8s-base:latest
FROM ${BREAKFIX_BASE_IMAGE}

COPY challenge.yaml /breakfix/challenge.yaml
COPY question.md /home/user/question.md
COPY generate.sh /breakfix/generate.sh
COPY verify.sh /verify.sh
COPY answer.sh /answer.sh
RUN chmod +x /breakfix/generate.sh /verify.sh /answer.sh
ENTRYPOINT ["/breakfix/runtime-init.sh"]
CMD ["sleep", "infinity"]
