# 题目系统设计

## 题目类型

### Script（写脚本）

用户编写脚本解决问题，verify 检查脚本执行结果。

```
例：压缩日志
  浏览器终端 → cat question.md → vim cleanup.sh → 跑一下 → submit
```

### Break-Fix（修故障）

环境已被破坏，用户排查并修复。verify 检查系统状态。

```
例：nginx 配置错误导致 502
  浏览器终端 → cat question.md → 排查 → 修配置 → reload → submit
```

---

## 题目文件结构

```
challenges/<id>/
├── challenge.yaml     # 元数据
├── Dockerfile         # FROM base + COPY question.md + RUN generate.sh
├── generate.sh        # 注入故障 (docker build 时执行)
├── question.md        # 用户看到的任务说明书
├── verify.sh          # 验收脚本 (server 持有, 不进镜像)
└── answer.sh          # 标准答案 (agent 自验证用, 不进镜像)
```

### challenge.yaml

```yaml
id: cleanup-logs
type: script
title: "批量压缩旧日志"
difficulty: easy
tags: [linux, find, tar, shell]
timeout: 600
image: cleanup-logs:v1
description: |
  服务器磁盘空间不足，/var/log 下有大量历史日志文件。
  你的任务：编写 /usr/local/bin/cleanup.sh，实现：
  1. 找出 /var/log 下 7 天前（mtime > 6 天）、大于 100MB 的 .log 文件
  2. 批量压缩到 /backup 目录（用 tar.gz 格式）
  3. 原文件可以不保留
```

> image 只需写短名称，Server 自动拼 `{registry}/{acr_namespace}/` 前缀。

### Dockerfile

```dockerfile
ARG BREAKFIX_BASE_IMAGE=<registry>/breakfix-base:latest
FROM ${BREAKFIX_BASE_IMAGE}
COPY question.md /home/user/question.md
COPY generate.sh /tmp/generate.sh
RUN bash /tmp/generate.sh && rm /tmp/generate.sh
```

### generate.sh

制造故障或准备测试数据，docker build 时执行。

### question.md

用户登录 Pod 后看到的任务说明书。

### verify.sh

submit 时由 server 拷贝到 Pod 内执行。exit 0 = 通过。

### answer.sh

Agent 自验证用的标准答案。不进镜像、不外泄。

---

## 用户流程

```
Web UI start challenge
  → Pod 启动（破损环境，question.md 在 ~/）
  → 浏览器终端连接 → 看 question.md → 排查/写脚本
  → Web UI submit
     → server kubectl cp verify.sh pod:/tmp/
     → server kubectl exec -- bash /tmp/verify.sh
     → exit 0 = PASS / 非 0 = FAIL
```

## Agent 自动生成

见 AGENT_WORKFLOW.md。
