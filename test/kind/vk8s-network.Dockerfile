FROM busybox:1.36.1

# Build a single-platform test image so Kind does not import BusyBox's
# multi-platform index with ctr --all-platforms.
RUN true
