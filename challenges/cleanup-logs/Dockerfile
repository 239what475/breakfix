FROM breakfix-base:latest

# Generate sample log files with various ages and sizes
COPY gen-files.sh /tmp/gen-files.sh
RUN bash /tmp/gen-files.sh && rm /tmp/gen-files.sh

# User's task: write /usr/local/bin/cleanup.sh
# (starts empty - they create it)
