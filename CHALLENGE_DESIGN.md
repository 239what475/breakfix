# 题目系统设计

## 题目类型

### Script（写脚本）

用户编写脚本解决问题，verify 检查脚本执行结果。

```
例：压缩日志
  breakfix ssh → vim cleanup.sh → 跑一下 → submit
```

### Break-Fix（修故障）

环境已被破坏，用户排查并修复。verify 检查系统状态。

```
例：nginx 配置错误导致 502
  breakfix ssh → 排查 → 修配置 → reload → submit
```

---

## 题目 Spec

```yaml
# challenge.yaml
id: cleanup-logs
type: script
title: "批量压缩旧日志"
difficulty: easy
tags: [linux, find, tar, shell]
timeout: 600
image: registry.cn-hangzhou.aliyuncs.com/breakfix/cleanup-logs:v1

description: |
  服务器磁盘空间不足，/var/log 下有大量历史日志。
  请编写 /usr/local/bin/cleanup.sh，实现：
  1. 找出 /var/log 下 7 天前、大于 100MB 的 log 文件
  2. 批量压缩到 /backup 目录

verify:
  script: |
    #!/bin/bash
    [ -x /usr/local/bin/cleanup.sh ] || exit 1
    bash /usr/local/bin/cleanup.sh
    find /var/log -type f -mtime +6 -size +100M | while IFS= read -r f; do
      name=$(basename "$f")
      ls /backup/${name%.log}*.tar.gz >/dev/null 2>&1 || exit 1
    done
    exit 0
```

---

## 目录结构

```
challenges/
├── cleanup-logs/
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
├── nginx-502/
│   ├── challenge.yaml
│   ├── Dockerfile
│   └── verify.sh
└── ...
```

---

## 题目构建

```bash
cd challenges/cleanup-logs
docker build -t registry.cn-hangzhou.aliyuncs.com/breakfix/cleanup-logs:v1 .
docker push registry.cn-hangzhou.aliyuncs.com/breakfix/cleanup-logs:v1
```

---

## 提交验证

`breakfix submit` 时：
1. API Server 通过 `kubectl cp` 将 `verify.sh` 注入 Pod
2. `kubectl exec` 运行 `verify.sh`
3. 退出码 0 = 通过，非 0 = 失败
4. 销毁 Pod + Namespace
