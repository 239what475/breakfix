# API Server 设计

## 认证

两个 gRPC 端口：

| 端口 | TLS | 用途 |
|------|------|------|
| 9090 | 明文 | Register / Login |
| 9533 | mTLS | 所有其他 RPC |

### 注册

```
CLI → Register(username, password) → Server
  → bcrypt 哈希密码
  → 生成 TOTP secret + QR 码
  → 存入 SQLite
  → 返回 secret + QR
```

### 登录

```
CLI → Login(username, password, totp) → Server
  → 验证 bcrypt 密码
  → 验证 TOTP (pquerna/otp)
  → CA 签发 12h 客户端证书 (crypto/x509)
  → 返回证书 + 私钥
```

### mTLS 验证

```go
func authRequireCert(ctx, req, info, handler) {
    peer := peer.FromContext(ctx)
    tlsInfo := peer.AuthInfo.(credentials.TLSInfo)
    cert := tlsInfo.State.PeerCertificates[0]
    subject := cert.Subject.CommonName  // "username"
    // 查数据库确认用户存在
    return handler(ctx, req)
}
```

---

## 数据库 Schema

```sql
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    subject       TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    password_hash TEXT NOT NULL DEFAULT '',
    totp_secret   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE challenges (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    type        TEXT NOT NULL,       -- script | break-fix
    difficulty  TEXT NOT NULL,
    tags        TEXT NOT NULL DEFAULT '[]',
    description TEXT NOT NULL,
    timeout     INTEGER NOT NULL DEFAULT 600,
    image       TEXT NOT NULL,
    dir_path    TEXT NOT NULL
);

CREATE TABLE instances (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    challenge_id TEXT NOT NULL REFERENCES challenges(id),
    status       TEXT NOT NULL DEFAULT 'running',
    namespace    TEXT NOT NULL,
    pod_name     TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    destroyed_at TEXT
);

CREATE TABLE submissions (
    id           TEXT PRIMARY KEY,
    instance_id  TEXT NOT NULL REFERENCES instances(id),
    user_id      TEXT NOT NULL REFERENCES users(id),
    challenge_id TEXT NOT NULL REFERENCES challenges(id),
    passed       INTEGER NOT NULL,
    exit_code    INTEGER NOT NULL,
    output       TEXT NOT NULL DEFAULT ''
);
```

---

## gRPC Proto

```protobuf
service Breakfix {
    // Auth (plain port)
    rpc Register (RegisterRequest) returns (RegisterResponse);
    rpc Login (LoginRequest) returns (LoginResponse);

    // Challenges
    rpc ListChallenges (ListChallengesRequest) returns (ListChallengesResponse);

    // Instances
    rpc StartChallenge (StartChallengeRequest) returns (StartChallengeResponse);
    rpc GetInstance (GetInstanceRequest) returns (GetInstanceResponse);
    rpc PingInstance (PingInstanceRequest) returns (PingInstanceResponse);
    rpc StopChallenge (StopChallengeRequest) returns (StopChallengeResponse);

    // Terminal (bidirectional stream)
    rpc ExecInstance (stream PTYData) returns (stream PTYData);

    // Submission
    rpc SubmitChallenge (SubmitChallengeRequest) returns (SubmitChallengeResponse);
}
```

---

## 冷静期

```
用户 exit → Pod 状态 → draining（5 分钟定时器）
  │
  ├── breakfix ssh → PingInstance → 重置为 running
  ├── breakfix submit → 验证 → 销毁
  └── 5 分钟到 → CooldownManager.destroy → 清理 Pod/NS/记录
```

`CooldownManager` 在 Server 启动时注册 cleanup 回调，定时器到期后调用 `Server.CleanupInstance`。
