# Breakfix 本地开发指南

## 依赖

- Go 1.24+
- Docker（Kind K8s 集群）
- kubectl
- kind CLI

## 快速开始

```bash
# 1. 创建 Kind 集群
kind create cluster --name breakfix-dev

# 2. 构建基础镜像
docker build -t breakfix-base:latest ./base
kind load docker-image breakfix-base:latest --name breakfix-dev

# 3. 构建题目镜像
cd challenges/cleanup-logs
docker build -t breakfix-cleanup-logs:dev .
kind load docker-image breakfix-cleanup-logs:dev --name breakfix-dev
cd ../..

# 4. 编译
go build -o bin/breakfix-api ./cmd/server
go build -o bin/breakfix-cli ./cmd/cli

# 5. 链接题目目录
mkdir -p data && ln -sf ../challenges data/challenges

# 6. 启动 API Server
./bin/breakfix-api

# 7. 另一个终端
./bin/breakfix-cli --server=localhost:9090 -u dev -p 123456 register
./bin/breakfix-cli --server=localhost:9090 -u dev -p 123456 login -t <totp>
./bin/breakfix-cli --server=localhost:9533 list
./bin/breakfix-cli --server=localhost:9533 start cleanup-logs
./bin/breakfix-cli --server=localhost:9533 ssh <id>
./bin/breakfix-cli --server=localhost:9533 submit <id>
```

## 构建

```bash
# 开发（带调试符号）
go build -o bin/breakfix-api ./cmd/server
go build -o bin/breakfix-cli ./cmd/cli

# 生产（去除符号）
go build -ldflags "-s -w -X github.com/breakfix/breakfix/internal/build.Version=v0.1.0" -o bin/breakfix-api ./cmd/server
```

## Lint

```bash
golangci-lint run ./...
```

## CI

推送 tag 触发 GitHub Actions 自动构建发布：

```bash
git tag v0.1.0
git push origin v0.1.0
```
