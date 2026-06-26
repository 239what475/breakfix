# Breakfix 本地开发指南

## 开发环境

只需要三样：

```
Docker + Go + kind CLI
```

不需要 Teleport、Tinyproxy、ACR、阿里云任何东西。

---

## 环境搭建

```bash
# 1. 创建本地 K8s（单节点 Docker 里的集群）
kind create cluster --name breakfix-dev

# 2. 构建基础镜像
docker build -t breakfix-base:latest ./base/

# 3. 加载进 Kind
kind load docker-image breakfix-base:latest --name breakfix-dev

# 4. 编译并启动 API Server
go build -ldflags "-X main.Mode=dev" -o breakfix-api-dev ./cmd/server
./breakfix-api-dev

# 5. 编译 CLI
go build -ldflags "-X main.Mode=dev" -o breakfix-dev ./cmd/cli
```

---

## go build flag 体系

```go
// internal/build/build.go
package build

var (
    Version   = "dev"
    BuildTime = "unknown"
    Commit    = "unknown"
    Mode      = "dev" // dev | prod
)

func IsDev() bool { return Mode == "dev" }
```

编译：

```bash
# 开发
go build -ldflags "
  -X 'github.com/your-org/breakfix/internal/build.Version=0.1.0'
  -X 'github.com/your-org/breakfix/internal/build.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'
  -X 'github.com/your-org/breakfix/internal/build.Commit=$(git rev-parse --short HEAD)'
  -X 'github.com/your-org/breakfix/internal/build.Mode=dev'
" -o breakfix-api-dev ./cmd/server

# 生产
go build -ldflags "
  -X '...Mode=prod'
" -o breakfix-api ./cmd/server
```

### Mode 控制的行为

| 行为 | dev | prod |
|------|-----|------|
| gRPC 认证 | **无**，metadata 里读 `subject: dev-user` | **mTLS**，验证 Teleport CA 签发的客户端证书 |
| K8s 连接 | kubectl 默认（指向 kind） | kubeconfig 文件 |
| 日志级别 | Debug | Info |
| 日志格式 | 人类可读 | JSON |
| 题目路径 | `./challenges/`（本地目录） | `/var/lib/breakfix/challenges/`（Git clone） |
| SQLite 路径 | 当前目录 `breakfix.db` | `/var/lib/breakfix/breakfix.db` |
| SSH (`breakfix ssh`) | `kubectl exec -ti <pod> -- /bin/bash` | 内嵌 tsh exec |

---

## 开发循环

```bash
# 每次改完代码：

go build -ldflags "-X main.Mode=dev" -o breakfix-api-dev ./cmd/server
go build -ldflags "-X main.Mode=dev" -o breakfix-dev ./cmd/cli

./breakfix-api-dev &
./breakfix-dev start cleanup-logs
./breakfix-dev ssh abc123
# 做题...
./breakfix-dev submit abc123
```

---

## 构建题目镜像（开发）

```bash
# 第一种：docker build + load 进 Kind
cd challenges/cleanup-logs
docker build -t breakfix-cleanup-logs:dev .
kind load docker-image breakfix-cleanup-logs:dev --name breakfix-dev

# 第二种：用 kind 的本地 registry（Kind 自带）
docker tag breakfix-cleanup-logs:dev localhost:5001/breakfix/cleanup-logs:dev
docker push localhost:5001/breakfix/cleanup-logs:dev
```

---

## 目录结构

```
开发机
├── cmd/
│   ├── server/main.go
│   └── cli/main.go
├── internal/
│   ├── build/build.go       ← Version, Mode 等
│   ├── server/               ← API Server 逻辑
│   ├── cli/                  ← CLI 命令
│   └── db/                   ← SQLite 操作
├── challenges/               ← 题目目录（本地）
│   └── cleanup-logs/
│       ├── challenge.yaml
│       ├── Dockerfile
│       └── verify.sh
├── go.mod
├── go.sum
└── Makefile                  ← 常用命令封装
```

### Makefile

```makefile
.PHONY: dev-server dev-cli prod

LDFLAGS_DEV = -ldflags "\
  -X 'github.com/your-org/breakfix/internal/build.Version=0.1.0' \
  -X 'github.com/your-org/breakfix/internal/build.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'github.com/your-org/breakfix/internal/build.Commit=$(shell git rev-parse --short HEAD)' \
  -X 'github.com/your-org/breakfix/internal/build.Mode=dev'"

LDFLAGS_PROD = -ldflags "\
  -X 'github.com/your-org/breakfix/internal/build.Version=$(VER)' \
  -X 'github.com/your-org/breakfix/internal/build.BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'github.com/your-org/breakfix/internal/build.Commit=$(shell git rev-parse --short HEAD)' \
  -X 'github.com/your-org/breakfix/internal/build.Mode=prod'"

dev-server:
	go build $(LDFLAGS_DEV) -o bin/breakfix-api-dev ./cmd/server

dev-cli:
	go build $(LDFLAGS_DEV) -o bin/breakfix-dev ./cmd/cli

dev: dev-server dev-cli

prod:
	go build $(LDFLAGS_PROD) -o bin/breakfix-api ./cmd/server
	go build $(LDFLAGS_PROD) -o bin/breakfix-cli ./cmd/cli

kind-up:
	kind create cluster --name breakfix-dev
	kind load docker-image breakfix-base:latest --name breakfix-dev

kind-down:
	kind delete cluster --name breakfix-dev

run: dev
	./bin/breakfix-api-dev

clean:
	rm -rf bin/

lint:
	golangci-lint run ./...

```

---

## 在开发环境写题目

挑战目录直接放 `./challenges/`。API Server 在 dev 模式下读当前目录，不改代码直接改 challenge.yaml + verify.sh + Dockerfile，即时生效。
