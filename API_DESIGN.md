# API Server 设计方案

## 1. 认证：mTLS + Teleport CA

### 原理

```
Teleport CA (网关 ECS 上)
    │
    │  私钥永远不离开服务器
    │
    └──▶ 签发短期证书（12 小时有效）
              │
              ▼
         CLI 持有证书
              │
              │  每次 gRPC 请求带证书
              ▼
         API Server
              │  验证: 签名是否来自 Teleport CA？
              │  验证: 证书过期了吗？
              │  提取: subject = "github-what"
              ▼
         身份确认，不需要 user_id 参数
```

Teleport 登录后本地生成一个短期客户端证书（`~/.tsh/keys/`），已用于 SSH 认证。我们复用同一份证书做 gRPC 的 mTLS（双向 TLS）。

### 为什么安全

| 攻击方式 | 能成功吗 |
|------|------|
| CLI 伪造 user_id 参数 | ❌ 请求里根本没有 user_id |
| 自签一个假证书 | ❌ 没 Teleport CA 私钥，签不出来 |
| 偷了别人证书 | 12 小时自动过期，不能用了 |
| 中间人拦截 | ❌ TLS 加密 |

### API Server 端代码示意

```go
func (s *Server) whoami(ctx context.Context) (string, *db.User, error) {
    peer, ok := peer.FromContext(ctx)
    if !ok {
        return "", nil, status.Error(codes.Unauthenticated, "no peer info")
    }
    tlsInfo, ok := peer.AuthInfo.(credentials.TLSInfo)
    if !ok {
        return "", nil, status.Error(codes.Unauthenticated, "no TLS")
    }
    cert := tlsInfo.State.PeerCertificates[0]
    subject := cert.Subject.CommonName  // "github-what"

    user, err := s.db.GetOrCreateUser(subject)
    if err != nil {
        return "", nil, err
    }
    return subject, user, nil
}

func (s *Server) SubmitChallenge(ctx context.Context, req *pb.SubmitChallengeRequest) (*pb.SubmitChallengeResponse, error) {
    _, user, err := s.whoami(ctx)
    if err != nil {
        return nil, err
    }

    inst, _ := s.db.GetInstance(req.InstanceId)
    if inst.UserID != user.ID {
        return nil, status.Error(codes.PermissionDenied, "not your instance")
    }
    // ...
}
```

---

## 2. 数据库 Schema

```sql
-- 用户表：首次 login 时自动创建
CREATE TABLE users (
    id          TEXT PRIMARY KEY,          -- uuid
    subject     TEXT NOT NULL UNIQUE,       -- Teleport 身份: "github-what"
    name        TEXT NOT NULL,              -- 显示名: "what"
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- 题目表：从 Git 仓库同步的索引
CREATE TABLE challenges (
    id          TEXT PRIMARY KEY,          -- slug: "cleanup-logs"
    title       TEXT NOT NULL,              -- "批量压缩旧日志"
    type        TEXT NOT NULL,              -- script | break-fix | investigate
    difficulty  TEXT NOT NULL,              -- easy | medium | hard
    tags        TEXT NOT NULL,              -- JSON array: ["linux","find","tar"]
    description TEXT NOT NULL,              -- Markdown 题目描述
    timeout     INTEGER NOT NULL DEFAULT 600,
    git_path    TEXT NOT NULL,              -- Git 仓库中的相对路径
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- 做题实例：每次 start 创建一条
CREATE TABLE instances (
    id          TEXT PRIMARY KEY,          -- 短 id: "abc123"
    user_id     TEXT NOT NULL REFERENCES users(id),
    challenge_id TEXT NOT NULL REFERENCES challenges(id),
    status      TEXT NOT NULL DEFAULT 'running',  -- running | draining | destroyed
    namespace   TEXT NOT NULL,              -- K8s namespace
    pod_name    TEXT NOT NULL,              -- K8s pod name
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    destroyed_at TEXT
);

-- 提交记录
CREATE TABLE submissions (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id),
    user_id     TEXT NOT NULL REFERENCES users(id),
    challenge_id TEXT NOT NULL REFERENCES challenges(id),
    passed      INTEGER NOT NULL,          -- 0 或 1
    exit_code   INTEGER NOT NULL,          -- verify 脚本退出码
    output      TEXT NOT NULL DEFAULT '',   -- verify 输出（截断）
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_instances_user ON instances(user_id, status);
CREATE INDEX idx_submissions_user ON submissions(user_id, created_at);
```

### 关系

```
users 1 ──┐
           ├── instances ── challenges
           │       │
           └── submissions ── challenges
```

---

## 3. gRPC Proto

```protobuf
syntax = "proto3";
package breakfix;

service Breakfix {
    // 用户
    rpc WhoAmI (WhoAmIRequest) returns (WhoAmIResponse);

    // 题目
    rpc ListChallenges (ListChallengesRequest) returns (ListChallengesResponse);

    // 实例
    rpc StartChallenge (StartChallengeRequest) returns (StartChallengeResponse);
    rpc GetInstance (GetInstanceRequest) returns (GetInstanceResponse);
    rpc PingInstance (PingInstanceRequest) returns (PingInstanceResponse);

    // 提交
    rpc SubmitChallenge (SubmitChallengeRequest) returns (SubmitChallengeResponse);
}

// ── 用户 ──
// 身份来自 mTLS 证书，请求体为空

message WhoAmIRequest {}

message WhoAmIResponse {
    string user_id = 1;
    string name = 2;
    bool is_new = 3;     // 首次 login 时为 true
}

// ── 题目 ──

message ListChallengesRequest {}

message ChallengeSummary {
    string id = 1;
    string title = 2;
    string type = 3;
    string difficulty = 4;
    repeated string tags = 5;
    bool solved = 6;         // 当前用户是否做过
}

message ListChallengesResponse {
    repeated ChallengeSummary challenges = 1;
}

// ── 实例 ──
// user_id 全删，身份来自 mTLS

message StartChallengeRequest {
    string challenge_id = 1;
}

message StartChallengeResponse {
    string instance_id = 1;
    string challenge_title = 2;
    int64 timeout_sec = 3;
}

message GetInstanceRequest {
    string instance_id = 1;
}

enum InstanceStatus {
    RUNNING = 0;
    DRAINING = 1;
    DESTROYED = 2;
}

message GetInstanceResponse {
    string instance_id = 1;
    string challenge_id = 2;
    InstanceStatus status = 3;
    int64 remaining_sec = 4;   // 冷静期剩余秒数
}

message PingInstanceRequest {
    string instance_id = 1;
}

message PingInstanceResponse {
    InstanceStatus status = 1;  // running | draining | destroyed
}

// ── 提交 ──

message SubmitChallengeRequest {
    string instance_id = 1;
}

message SubmitChallengeResponse {
    bool passed = 1;
    int32 exit_code = 2;
    string output = 3;
}
```

---

## 4. CLI 与 API Server 交互总览

```
用户                          CLI                           API Server
  │                            │                               │
  │ breakfix login             │                               │
  │ ──────────────────────▶   │  tsh login --proxy=...        │
  │                            │ ───▶ Teleport OAuth (GitHub)  │
  │                            │ ◀─── 短期证书                  │
  │                            │                               │
  │                            │  WhoAmI() + mTLS 证书         │  ← gRPC 双向 TLS
  │                            │ ──────────────────────────▶   │
  │                            │         验证证书 → 查/建用户    │
  │                            │         ◀── user_id + name    │
  │ ◀── ✓ Logged in as what    │                               │
  │                            │                               │
  │ breakfix list              │                               │
  │ ──────────────────────▶   │  ListChallenges() + 证书        │
  │                            │ ──────────────────────────▶   │
  │                            │         ◀── 题目列表            │
  │ ◀── 题目表格               │                               │
  │                            │                               │
  │ breakfix start cleanup-logs│                              │
  │ ──────────────────────▶   │  StartChallenge("cleanup-logs")│
  │                            │ ──────────────────────────▶   │
  │                            │          创建 NS + Pod         │
  │                            │         ◀── instance abc123    │
  │ ◀── Instance abc123 created│                               │
  │                            │                               │
  │ breakfix ssh abc123        │                               │
  │ ──────────────────────▶   │  PingInstance("abc123")        │  ← CLI 自动续期
  │                            │ ──────────────────────────▶   │
  │                            │         ◀── status=running    │
  │                            │  tsh kubectl exec ...          │
  │                            │ ───────────────────▶ Pod      │
  │   (Teleport 直连 Pod)                                       │
  │                            │                               │
  │ breakfix submit abc123     │                              │
  │ ──────────────────────▶   │  SubmitChallenge("abc123")     │
  │                            │ + 证书 ─────────────────────▶  │
  │                            │     注入 verify.sh → exec     │
  │                            │         ◀── passed + output    │
  │ ◀── ✓ PASSED               │                               │
```

所有 gRPC 请求都不带 `user_id`，身份在 TLS 握手阶段由证书确定。

---

## 5. 题目同步

API Server 启动时（或收到 reload 信号时）：

```
Git 仓库 (本地 clone 或挂载)
    │
    ▼
遍历 challenges/*/challenge.yaml
    │
    ▼
逐条 upsert 到 SQLite challenges 表
    │
    ▼
list 就查 SQLite，不读文件系统
```

Dockerfile + verify.sh 不存数据库。build 镜像时从 Git 读 Dockerfile，submit 时从 Git 读 verify.sh。

---

## 6. 启动流程

```
网关 ECS 上:

1. systemctl start teleport     # Teleport Proxy (CA + Auth + SSH)
2. systemctl start tinyproxy    # 出网代理
3. systemctl start breakfix-api # API Server (Go binary, gRPC + mTLS)
4. systemctl start nginx        # CLI 二进制分发

API Server 启动:
  → 连接 SQLite
  → 从 Git 同步题目索引
  → 连 K8s (kubeconfig + 内网)
  → 开始接受 gRPC (配置 Teleport CA 证书做 mTLS 验证)
```

API Server 需要**配置 Teleport CA 的公钥证书**以验证客户端证书签名。公钥可以安全分发（放在网关 ECS 上就行）。

---

## 7. 冷静期实现

```
用户 exit
  │
  ▼
Pod 状态 → draining（5 分钟定时器启动）
  │
  ├── 用户 breakfix ssh <id>
  │     │
  │     ▼
  │   CLI 自动调 PingInstance → API Server 检测 status=draining
  │     │                           → 重置为 running，取消定时器
  │     ▼
  │   CLI 再执行 tsh kubectl exec → 用户进 Pod
  │
  ├── breakfix submit → 注入 verify.sh → exec → 判 → 销毁
  │
  └── 5 分钟到了 → 销毁
```

CLI 在 `breakfix ssh`、`breakfix status` 时自动调 `PingInstance`，用户无感知。
