# 题目系统设计

## 三种题目类型

### 1. Script（写脚本）

用户进入环境，编写脚本解决问题。verify 检查脚本执行结果。

```
例：压缩日志
  用户 ssh 进去 → 编辑器写 cleanup.sh → 跑一下 → submit
```

### 2. Break-Fix（修故障）

环境已经被故意破坏，用户排查并修复。verify 检查系统状态是否恢复正常。

```
例：nginx 配置错误导致 502
  用户 ssh 进去 → 排查 nginx → 修配置 → reload → submit
```

### 3. Investigate（排查分析）

环境有问题，用户排查并写出结论。不修东西，只定位根因。

```
例：CPU 飙到 90%
  用户 ssh 进去 → top/strace/ps 排查 → 写 /tmp/answer.txt → submit
```

---

## 对比

| | Script | Break-Fix | Investigate |
|------|------|------|------|
| 环境初始状态 | 干净的 | **坏的** | **坏的** |
| 用户做什么 | 写脚本 | 找问题 + 修复 | 找问题 + 写结论 |
| submit 时验证 | 脚本存在 + 功能正确 | 系统状态恢复 | answer 内容正确 |
| verify 执行方式 | 跑用户脚本 → 检查结果 | 检查系统状态 | 读用户答题文件 |
| 依赖会话录制 | 不需要 | 不需要 | 可选（人工看过程） |
| 难度范围 | easy ~ medium | medium ~ hard | medium ~ hard |

---

## 题目 Spec

```yaml
# challenge.yaml
id: cleanup-logs
type: script                      # script | break-fix | investigate
title: "批量压缩旧日志"
difficulty: easy
tags: [linux, find, tar, shell]
timeout: 600

description: |
  服务器磁盘空间不足，/var/log 下有大量历史日志。
  请编写 /usr/local/bin/cleanup.sh，实现：
  1. 找出 /var/log 下 7 天前、大于 100MB 的 log 文件
  2. 批量压缩到 /backup 目录
  3. 原文件可以不保留

setup:
  dockerfile: |
    FROM registry.cn-hangzhou.aliyuncs.com/breakfix/base:ubuntu-22.04
    COPY gen-logs.sh /tmp/
    RUN bash /tmp/gen-logs.sh && rm /tmp/gen-logs.sh

verify:
  type: script                    # script: 跑用户脚本+检查结果
                                  # break-fix: 只检查系统状态
                                  # investigate: 读 /tmp/answer.txt
  script: |
    #!/bin/bash
    # 1. 脚本必须存在且可执行
    [ -x /usr/local/bin/cleanup.sh ] || exit 1

    # 2. 跑用户脚本
    bash /usr/local/bin/cleanup.sh

    # 3. 检查结果：符合条件的文件都有压缩包
    find /var/log -type f -mtime +6 -size +100M | while read f; do
      name=$(basename "$f")
      ls /backup/${name}*.tar.gz >/dev/null 2>&1 || exit 1
    done

    # 4. 检查不该压缩的没被碰
    find /var/log -type f -name "*.log" \( -mtime -7 -o -size -100M \) | while read f; do
      name=$(basename "$f")
      ls /backup/${name}*.tar.gz >/dev/null 2>&1 && exit 1
    done

    exit 0
```

### Break-Fix 例子

```yaml
id: nginx-502
type: break-fix
title: "修复 Nginx 502 错误"
difficulty: medium
tags: [nginx, troubleshooting, config]
timeout: 900

description: |
  用户反馈网站返回 502 Bad Gateway。
  Nginx 正在运行但配置有问题。请修复它。

# setup 里制造故障：nginx 反向代理指向了错误的端口
# 用户修复后系统恢复正常

verify:
  type: break-fix                  # 不需要跑用户脚本
  script: |
    #!/bin/bash
    # 检查 nginx 正常运行
    nginx -t || exit 1
    systemctl is-active nginx || exit 1
    # 检查返回 200
    curl -s -o /dev/null -w "%{http_code}" http://localhost | grep -q 200 || exit 1
```

### Investigate 例子

```yaml
id: cpu-90-percent
type: investigate
title: "CPU 飙到 90%，找出原因"
difficulty: medium
tags: [cpu, process, troubleshooting]
timeout: 1200

description: |
  服务器 CPU 使用率异常升高到 90% 以上。
  请排查原因，将结论写到 /tmp/answer.txt。
  格式：进程名 + 根因 + 解决建议

verify:
  type: investigate
  # investigate 用 answer_validator 替代 script
  answer_path: /tmp/answer.txt
  expected_keywords:
    - "stress-ng"      # 或具体的恶意进程名
    - "cpu"
  # 也可以是一段脚本解析 answer，exit 0 表示正确
  script: |
    #!/bin/bash
    [ -f /tmp/answer.txt ] || exit 1
    grep -qi "cpu" /tmp/answer.txt || exit 1
    grep -qiE "(stress|恶意|异常进程)" /tmp/answer.txt || exit 1
```

---

## 题目存储

```
git repo: breakfix/challenges
├── cleanup-logs/          ← script 类
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
├── nginx-502/             ← break-fix 类
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
├── cpu-90-percent/        ← investigate 类
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
└── ...
```

每种类型的 verify 执行策略不同：

| verify type | API Server 做什么 |
|-----|------|
| script | `exec ./verify.sh`（先跑用户脚本再检查） |
| break-fix | `exec ./verify.sh`（直接检查系统状态） |
| investigate | `exec ./verify.sh`（检查 /tmp/answer.txt） |

**关键设计**：三种类型最终都是一条 verify.sh，只是脚本内容不同。API Server 不需要区分类型——它只需要做 `submit = 注入 verify.sh + exec + 拿退出码`。

---

## 前期路线

| 阶段 | |
|------|------|
| 先做 | **Script** + **Break-Fix**，用 verify.sh 退出码判定 |
| 第二阶段 | **Investigate** + 接入 Teleport 会话录制做过程评判 |
| 以后 | 混合类型（先 investigate 定位 + 再 break-fix 修） |
