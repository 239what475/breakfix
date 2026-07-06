#!/bin/bash
set -euo pipefail

cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: default
spec:
  selector:
    app: web
  ports:
    - port: 80
      targetPort: 8080
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  replicas: 2
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: nginx:1.99
          ports:
            - containerPort: 8080
EOF

kubectl rollout status deployment/web -n default --timeout=10s >/tmp/breakfix-generate-rollout.log 2>&1 || true
