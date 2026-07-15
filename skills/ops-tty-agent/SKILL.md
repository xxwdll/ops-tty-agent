---
name: ops-tty-agent
description: 远程命令执行和文件操作代理。当用户需要查看远程服务器日志、执行命令、上传下载文件、排查问题、或在多个不互通的节点间传输文件时使用此技能。
---

# ops-tty-agent - AI 调用指南

**这是给 AI 看的文档**。告诉 AI 什么时候调什么接口、参数怎么传、返回怎么判断。

**SOP（怎么启动服务）见文档底部**，那是给客户看的。

---

## 一、决策流程：先判断场景，再选接口

### 场景 0：通过跳板节点 B 操作下游节点 C
B 启动为本地模式，客户端在请求中携带目标信息即可动态路由：

- **POST /cmd**：body 中加 `target` 和 `target_token` 字段
- **GET /tail、/stat、/disk、/download**：Header 加 `X-Target` 和 `X-Target-Token`
- **PUT /upload**：Header 加 `X-Target` 和 `X-Target-Token`

**核心原则**：B 是无状态路由器，一个 B 可同时代理 C1、C2、C3...多个下游节点，每个节点可有独立 token。

### 场景 1：查看日志文件
**不要** `bash -c "tail -n 100 /var/log/xxx"` ❌
**要** `GET /tail?path=/var/log/xxx&lines=100` ✅

理由：返回结构化行数组，自动返回文件大小，不需要解析文本。

### 场景 2：查看文件/目录信息
**不要** `bash -c "ls -la /xxx"` ❌
**要** `GET /stat?path=/xxx` ✅

理由：返回结构化 JSON（大小、权限、owner、修改时间），跨平台一致。

### 场景 3：查看磁盘使用
**不要** `bash -c "df -h"` ❌
**要** `GET /disk` ✅

理由：返回结构化数组，含字节数和人类可读两种格式，不需要解析 `df` 的文本输出。

### 场景 4：执行复杂命令（grep、awk、sed、管道等）
**用** `POST /cmd` ✅

### 场景 5：从 B 节点传文件到 C 节点（B 和 C 不互通）
**用** `POST /transfer` ✅

A（本机）做中转，不落盘，流式传输。

### 场景 6：上传/下载文件
**用** `PUT /upload/:filename` / `GET /download/:filename` ✅

---

## 二、接口详情

所有请求 Header 必须携带：`X-Token: <token>`

**动态代理**（通过 B 节点跳转到 C 节点）：
- `POST /cmd`：在 JSON body 中加 `target` / `target_token` 字段
- `PUT /upload`：在 Header 中加 `X-Target` / `X-Target-Token`
- GET 请求：在 Header 中加 `X-Target: <url>` / `X-Target-Token: <token>`（可选）
- B 节点无需 `--target` 参数，纯本地模式即可同时代理多个下游节点

### POST /cmd — 执行命令

**什么时候用**：专用接口覆盖不了的复杂操作（grep、awk、管道、安装软件等）

**请求体**：
```json
{
  "cmd": "df -h",
  "timeout_seconds": 60,
  "target": "http://C1:8081",
  "target_token": "c1-token"
}
```
- `cmd`：要执行的命令字符串（必填）
- `timeout_seconds`：超时秒数（可选，默认 60s）。长命令如 `yum install` 可设 300
- `target`：动态指定下一跳节点 URL（可选）。B 收到后会转发到该节点
- `target_token`：下一跳节点的认证 token（可选，不填则用当前 B 节点的 token）

**返回**：
```json
{
  "stdout": "...",
  "stderr": "",
  "exit_code": 0,
  "duration_ms": 150,
  "truncated": false,
  "error": ""
}
```

**判断规则（按优先级）**：
1. `error != ""` → 系统错误（超时、shell 不存在等），重试或检查环境
2. `exit_code != 0` → 命令执行失败，重点分析 `stderr`
3. `exit_code == 0` → 成功，分析 `stdout`
4. `truncated == true` → 输出超过 16MB 被截断，信息不完整，应缩小查询范围
5. `stderr != ""` 但 `exit_code == 0` → 不是失败，只是命令写了 stderr（如 `grep` 无匹配）

---

### GET /tail — 读取文件尾部

**什么时候用**：看日志文件

**URL 参数**：
- `path`：文件绝对路径（必填）
- `lines`：返回行数（可选，默认 100）
- `max_bytes`：最多读取的字节数（可选，默认 1MB）

**示例**：
```
GET /tail?path=/var/log/syslog&lines=200&max_bytes=2097152
```

**返回**：
```json
{
  "lines": ["line1", "line2", "..."],
  "total_lines_returned": 200,
  "file_size": 52428800,
  "truncated": false,
  "error": ""
}
```

---

### GET /stat — 查看文件/目录信息

**什么时候用**：检查文件大小、权限、owner、修改时间

**URL 参数**：
- `path`：文件/目录绝对路径（必填）

**示例**：
```
GET /stat?path=/var/log/syslog
```

**返回**：
```json
{
  "path": "/var/log/syslog",
  "type": "file",
  "size_bytes": 52428800,
  "size_human": "50.0 MB",
  "mtime": "2026-05-29T10:00:00+08:00",
  "mode": "-rw-r--r--",
  "owner": "root",
  "group": "adm",
  "error": ""
}
```

---

### GET /disk — 查看磁盘使用

**什么时候用**：排查磁盘空间问题

**示例**：
```
GET /disk
```

**返回**：
```json
{
  "filesystems": [
    {
      "filesystem": "/dev/sda1",
      "mountpoint": "/",
      "size_bytes": 107374182400,
      "size_human": "100.0 GB",
      "used_bytes": 53687091200,
      "used_human": "50.0 GB",
      "free_bytes": 53687091200,
      "free_human": "50.0 GB",
      "used_percent": "50%"
    }
  ],
  "error": ""
}
```

---

### PUT /upload/:filename — 上传文件

**什么时候用**：向远程节点上传脚本、配置文件等

**示例**：
```bash
# 上传到默认 uploads/ 目录
PUT /upload/test.txt
Header: X-Token: xxx
Body: <文件二进制内容>

# 上传到指定目录
PUT /upload/nginx.conf?dir=/etc/nginx/
Header: X-Token: xxx
Body: <文件二进制内容>
```

**参数**：
- `dir`：目标目录（可选，默认 `uploads/`，必须为绝对路径且不含 `..`）

**返回**：
```json
{"message":"文件上传成功","path":"/etc/nginx/nginx.conf"}
```

---

### GET /download/:filename — 下载文件

**什么时候用**：从远程节点下载文件

**示例**：
```bash
# 从 uploads/ 下载
GET /download/test.txt
Header: X-Token: xxx

# 从任意路径下载
GET /download/nginx.conf?path=/etc/nginx/nginx.conf
Header: X-Token: xxx
```

**参数**：
- `path`：文件绝对路径（可选，默认从 `uploads/` + URL 文件名读取）

**返回**：文件二进制内容（200）或 404

---

### POST /transfer — 跨节点文件传输

**什么时候用**：B 和 C 两个机房不互通，但 A（本机）能同时直连 B 和 C，需要把文件从 B 传到 C

**请求体**：
```json
{
  "source_url": "http://B:8080/download/data.tar.gz",
  "source_token": "token-b",
  "target_url": "http://C:8080/upload/data.tar.gz",
  "target_token": "token-c"
}
```
- `source_url` / `target_url`：完整 URL（必填）
- `source_token` / `target_token`：可选，不填则用当前服务 Token

**返回**：
```json
{
  "success": true,
  "bytes": 104857600,
  "duration_ms": 15234,
  "error": ""
}
```

**注意**：
- 必须在 A（本机）直接调用，不能通过代理链转发
- 不落盘，流式传输，支持大文件

---

### GET /history — 查看本次执行历史

**什么时候用**：想了解这个 agent 之前执行过什么命令

**示例**：
```
GET /history
```

**返回**：执行记录数组（含 cmd、stdout、success、duration_ms）

---

## 三、快速查表

| 你想做什么 | 调哪个接口 | 参数 |
|-----------|-----------|------|
| 看日志 | `GET /tail?path=...&lines=...` | path, lines, max_bytes |
| 看文件信息 | `GET /stat?path=...` | path |
| 看磁盘 | `GET /disk` | 无 |
| 执行命令 | `POST /cmd` | cmd, timeout_seconds, target(可选), target_token(可选) |
| 上传文件 | `PUT /upload/:filename?dir=...` | 文件内容, dir(可选) |
| 下载文件 | `GET /download/:filename?path=...` | path(可选) |
| B→C 传文件 | `POST /transfer` | source_url, target_url |
| 看执行历史 | `GET /history` | 无 |
| 动态跳转(GET) | 任意 GET 端点 + Header `X-Target` / `X-Target-Token` | 目标 URL 和 token |
| 动态跳转(POST) | POST /cmd body 中加 `target` / `target_token` | 目标 URL 和 token |

---

## 四、SOP（标准操作流程）—— 给客户看的

### 启动服务

```bash
# 本地模式（执行本地命令）
./ops-tty-agent --port 80 --shell bash --auto-confirm yes --token mytoken

# 代理模式（转发到目标节点）
./ops-tty-agent --port 80 --target http://b-node:8080 --token mytoken
```

### 命令行参数

| 参数 | 简写 | 说明 | 默认值 |
|------|------|------|--------|
| --port | -p | 服务端口 | 8080 |
| --shell | -s | shell类型 (bash/zsh/sh) | bash |
| --auto-confirm | -a | 自动确认 (yes/no) | no |
| --target | -t | 代理目标节点URL | 空 |
| --token | -k | 认证token | 随机生成 |
| --max-upload-size | -m | 最大上传文件大小 | 500MB |
| --proxy-timeout | | 代理超时时间（秒） | 30秒 |
| --enable-block-check | | 启用危险命令检查 | false |
| --block-commands | -b | 拦截命令列表（逗号分隔） | 空 |

### 代理模式（A→B 跳转）

**静态代理（启动时指定固定目标）**：
```bash
# A节点（代理模式）
./ops-tty-agent --port 80 --target http://b-node:8080 --token mytoken

# B节点（本地模式）
./ops-tty-agent --port 8080 --shell bash --token mytoken
```

**动态代理（B 做跳板，1 对多）**：
```bash
# B 节点（跳板，本地模式，无需 --target）
./ops-tty-agent --port 8080 --shell bash --auto-confirm yes --token b-token

# C1——db 服务器
./ops-tty-agent --port 8081 --shell bash --token c1-token

# C2——web 服务器
./ops-tty-agent --port 8082 --shell bash --token c2-token
```

通过 B 跳转到 C1 执行命令：
```bash
curl -X POST http://B:8080/cmd \
  -H "X-Token: b-token" \
  -d '{"cmd":"df -h","target":"http://C1:8081","target_token":"c1-token"}'
```

通过 B 查看 C2 磁盘（GET 端点用 Header）：
```bash
curl http://B:8080/disk \
  -H "X-Token: b-token" \
  -H "X-Target: http://C2:8082" \
  -H "X-Target-Token: c2-token"
```

通过 B 查看 C1 的日志：
```bash
curl "http://B:8080/tail?path=/var/log/syslog&lines=50" \
  -H "X-Token: b-token" \
  -H "X-Target: http://C1:8081" \
  -H "X-Target-Token: c1-token"
```

### 跨节点传输（B→C，A 做中转）

```bash
# A（本机）
./ops-tty-agent --port 80 --shell bash --auto-confirm yes --token mytoken

# B（源节点）
./ops-tty-agent --port 8080 --shell bash --auto-confirm yes --token mytoken

# C（目标节点）
./ops-tty-agent --port 8080 --shell bash --auto-confirm yes --token mytoken
```

然后调用 A 的 `/transfer`：
```bash
curl -X POST http://A:80/transfer \
  -H "X-Token: mytoken" \
  -d '{"source_url":"http://B:8080/download/file.tar.gz","target_url":"http://C:8080/upload/file.tar.gz"}'
```

### 安全说明

- `--auto-confirm yes`：适合 AI 自动化场景，无需人工确认
- `--auto-confirm no`：每次命令需终端输入 y/n，更安全
- 危险命令检查（可选）：`--enable-block-check --block-commands "rm -rf /,dd if="`

---

## 五、项目位置

本地项目：`/Users/user/Desktop/Code/ai/ops-tty-agent/`

编译：
```bash
cd /Users/user/Desktop/Code/ai/ops-tty-agent
go build -o ops-tty-agent
```

多架构编译：
```bash
./build.sh
```
