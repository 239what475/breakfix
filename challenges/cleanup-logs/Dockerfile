ARG BREAKFIX_BASE_IMAGE=breakfix-base:latest
FROM ${BREAKFIX_BASE_IMAGE}

# Generate sample log files with various ages and sizes
COPY question.md /home/user/question.md
COPY generate.sh /tmp/generate.sh
RUN bash /tmp/generate.sh && rm /tmp/generate.sh

# Include verify.sh for submission checking
COPY verify.sh /verify.sh
RUN chmod +x /verify.sh

# Include answer.sh for platform verification
COPY answer.sh /answer.sh
RUN chmod +x /answer.sh

# User's task: write /usr/local/bin/cleanup.sh
