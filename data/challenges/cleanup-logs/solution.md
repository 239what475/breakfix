# 解答：批量压缩旧日志

## 第一步：创建清理脚本

先创建脚本并使其可执行：

```bash
sudo mkdir -p /usr/local/bin
sudoedit /usr/local/bin/cleanup.sh
sudo chmod +x /usr/local/bin/cleanup.sh
```

## 第二步：只选择目标日志

`find` 的条件必须同时满足：文件是 `.log`、修改时间超过 7 天、大小超过 100MB。使用 `-print0` 可以安全处理带空格的文件名。

```bash
#!/usr/bin/env bash
set -euo pipefail

mkdir -p /backup
find /var/log -type f -name '*.log' -mtime +6 -size +100M -print0 |
  while IFS= read -r -d '' file; do
    base="$(basename "$file" .log)"
    tar -czf "/backup/${base}.tar.gz" -C "$(dirname "$file")" "$(basename "$file")"
  done
```

`-mtime +6` 表示至少完整过去 7 天。归档完成后运行脚本，再查看检查点。

## 第三步：验证保护范围

检查 `/backup` 只包含目标日志对应的归档：

```bash
find /backup -type f -name '*.tar.gz' -print
tar -tzf /backup/old-large-1.tar.gz
```

新日志和小日志应仍在 `/var/log` 中，也不应该出现对应归档。
