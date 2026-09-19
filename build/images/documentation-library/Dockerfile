# The documentation library image carries the immutable docs-project output.
# It is content only: no runtime, no entrypoint, digest-addressable. The
# deployment mounts the image root directly as the read-only library volume,
# so the corpus files live at the image root.
FROM scratch
COPY documents/ /
