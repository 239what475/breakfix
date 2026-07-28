# Verifier contains only the deterministic Kubernetes verification binary. It
# intentionally has no BuildKit, Registry write credential, Node.js, or model
# agent CLI.
FROM gcr.io/distroless/static-debian12:nonroot

COPY breakfix-verifier /usr/local/bin/verifier

ENTRYPOINT ["/usr/local/bin/verifier"]
