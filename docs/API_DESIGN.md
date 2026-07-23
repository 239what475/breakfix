# Gateway API 设计

## 认证

单端口 HTTP API + WebSocket 终端：

| 能力 | 机制 |
|------|------|
| Register / Login | HTTP JSON |
| 会话鉴权 | JWT Bearer token |
| 终端连接 | WebSocket + Authorization header |

### 注册

```
Browser → POST /auth/register → Gateway
  → 哈希密码
  → 生成 TOTP secret + QR 码
  → 存入 SQLite
  → 返回 secret + otpauth URL
```

### 登录

```
Browser → POST /auth/login → Gateway
  → 验证密码
  → 验证 TOTP (pquerna/otp)
  → 签发 JWT
  → 返回 token + user info
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

```

---

## gRPC Proto

```protobuf
service Breakfix {
    // Auth
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

    // Agent (future)
    // rpc GenerateChallenge (GenerateChallengeRequest) returns (GenerateChallengeResponse);
}

message RegisterRequest {
    string username = 1;
    string password = 2;
}

message RegisterResponse {
    string totp_secret = 1;
    string totp_qr = 2;
    string ca_cert = 3;          // CA 公钥 PEM
}

message LoginRequest {
    string username = 1;
    string password = 2;
    string totp_code = 3;
}

message LoginResponse {
    string user_id = 1;
    string name = 2;
    string client_cert = 3;
    string client_key = 4;
    string ca_cert = 5;          // CA 公钥 PEM
}
```

---

## 冷静期

```
用户断开浏览器终端 → Pod 状态 → draining（5 分钟定时器）
  │
  ├── 浏览器重新连接终端 → PingInstance → 重置为 running
  ├── 检查点全部通过 → 自动完成
  └── 5 分钟到 → CooldownManager.destroy → 清理 Pod/NS/记录
```

`CooldownManager` 在 Server 启动时注册 cleanup 回调，定时器到期后调用 `Server.CleanupInstance`。
