---
name: ops-tty-agent
description: 命令行代理服务 - 在本地代理远程命令执行和文件上传/下载，支持多级跳转代理和Token认证。当用户提及"ops-tty"、"代理命令"、"远程执行cmd"、"上传文件"、"下载文件"、"跳转代理"时使用此技能。
version: 1.4.0
---

# ops-tty-agent - 命令行代理服务

ops-tty-agent 是 go-tty 项目的升级版，用于在本地代理命令行执行，支持远程命令执行、文件上传/下载和多级跳转代理。

## 项目位置

项目位于: `/Users/user/Desktop/Code/ops-tty-agent/`

## 快速开始

### 编译项目
```bash
cd /Users/user/Desktop/Code/ops-tty-agent
go build -o ops-tty-agent
```

### 启动服务
```bash
# 本地模式（执行本地命令）
./ops-tty-agent --port 80 --shell bash --auto-confirm no

# 代理模式（纯转发，不执行本地命令）
./ops-tty-agent --port 80 --target http://b-node:8080
```

#### 启动参数
| 参数 | 简写 | 说明 | 默认值 |
|------|------|------|--------|
| --port | -p | 服务端口 | 8080 |
| --shell | -s | shell类型 (bash/zsh/sh) - 仅本地模式有效 | bash |
| --auto-confirm | -a | 自动确认 (yes=不审计, no=需确认) - 仅本地模式有效 | no |
| --target | -t | 代理目标节点URL - 启用后进入代理模式 | 空 |
| --token | -k | 认证token - 不指定则随机生成 | 随机生成 |
| --max-upload-size | -m | 最大上传文件大小（字节） | 500MB |
| --proxy-timeout | | 代理超时时间（秒） | 30秒 |
| --enable-block-check | | 启用危险命令检查 | false |
| --block-commands | -b | 要拦截的危险命令列表（逗号分隔） | 空 |

#### 模式说明
- **本地模式**：不指定 `--target`，在本地执行命令，支持 `--shell` 和 `--auto-confirm`
- **代理模式**：指定 `--target`，纯转发请求到目标节点，忽略 `--shell` 和 `--auto-confirm`

#### Token 认证
所有请求必须在 Header 中携带 `X-Token`：
```bash
curl -H "X-Token: your-token" http://localhost:80/cmd ...
```

- 指定 `--token`：使用固定 token
- 不指定：启动时随机生成 32 位 token，日志中显示

## 代理模式

### 多级跳转
当你只能访问A节点，但需要操作B节点时，可以通过A节点代理：

```
你 → A节点(ops-tty-agent) → B节点(ops-tty-agent) → 执行命令
```

**A节点启动（代理模式）：**
```bash
./ops-tty-agent --port 80 --target http://b-node:8080 --token mytoken
```

**B节点启动（本地模式）：**
```bash
./ops-tty-agent --port 8080 --shell bash --token mytoken
```

**注意：代理模式下，A节点会自动转发请求中的 Token 到 B节点，确保两端 Token 一致。**

### 多级链式代理
支持 A→B→C 多级跳转：
```bash
# A节点
./ops-tty-agent --port 80 --target http://b-node:8080 --token chain-token

# B节点
./ops-tty-agent --port 8080 --target http://c-node:8080 --token chain-token

# C节点
./ops-tty-agent --port 8080 --shell bash --token chain-token
```

**所有节点使用相同 Token，请求自动沿链路转发。**

## API 使用

**注意：所有请求必须在 Header 中携带 `X-Token`**

### 执行命令

**请求：**
```bash
curl -X POST http://localhost:80/cmd \
  -H "Content-Type: application/json" \
  -H "X-Token: your-token" \
  -d '{"cmd":"df -h"}'
```

**响应：**
```json
{
  "stdout": "Filesystem      Size  Used Avail Use% Mounted on\n...",
  "stderr": "",
  "exit_code": 0,
  "duration_ms": 150,
  "truncated": false
}
```

**重要：判断执行结果的规则（Claude 必须遵守）**
- `exit_code == 0` 表示命令执行成功（即使 stdout 为空）
- `exit_code != 0` 表示命令执行失败，重点分析 `stderr` 和 `error`
- `stderr != ""` 不等于失败，很多命令（如 `grep` 无匹配）会写 stderr 但 exit_code=0
- `truncated == true` 表示输出被截断了（超过 16MB），信息不完整，应改用更精确的命令
- `error != ""` 表示系统级错误（如 shell 不存在、超时），不是命令本身的错误

### 读取文件尾部（推荐替代 `tail` 命令）

**请求：**
```bash
curl -H "X-Token: your-token" "http://localhost:80/tail?path=/var/log/syslog&lines=100&max_bytes=1048576"
```

**响应：**
```json
{
  "lines": ["May 29 10:00:01 host cron[1234]: ...", "May 29 10:00:02 host sshd[5678]: ..."],
  "total_lines_returned": 100,
  "file_size": 52428800,
  "truncated": false
}
```

### 查看文件信息（推荐替代 `ls -la`）

**请求：**
```bash
curl -H "X-Token: your-token" "http://localhost:80/stat?path=/var/log/syslog"
```

**响应：**
```json
{
  "path": "/var/log/syslog",
  "type": "file",
  "size_bytes": 52428800,
  "size_human": "50.0 MB",
  "mtime": "2026-05-29T10:00:00+08:00",
  "mode": "-rw-r--r--",
  "owner": "root",
  "group": "adm"
}
```

### 查看磁盘使用（推荐替代 `df -h`）

**请求：**
```bash
curl -H "X-Token: your-token" http://localhost:80/disk
```

**响应：**
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
  ]
}
```

### 上传文件

**方法1：URL路径指定文件名**
```bash
curl -X PUT http://localhost:80/upload/test.txt \
  -H "X-Token: your-token" \
  --data-binary @test.txt
```

**方法2：Content-Disposition指定文件名**
```bash
curl -X POST http://localhost:80/upload \
  -H "Content-Disposition: attachment; filename=\"test.txt\"" \
  -H "X-Token: your-token" \
  --data-binary @test.txt
```

**响应：**
```json
{"message":"文件上传成功","path":"uploads/test.txt"}
```

文件保存位置：`uploads/` 目录（代理模式下保存到最终目标节点）

### 下载文件

**请求：**
```bash
curl -X GET http://localhost:80/download/test.txt \
  -H "X-Token: your-token" \
  -o test.txt
```

**响应：**
- 成功：返回文件内容，状态码 200
- 文件不存在：`{"error":"文件不存在"}`，状态码 404

文件下载位置：`uploads/` 目录（代理模式下从最终目标节点获取）

### 跨节点文件传输（B → C，A 做中转）

**适用场景**：B 和 C 两个机房不互通，但 A（你的电脑）能同时直连 B 和 C。

**启动方式**：
```
A（你的电脑）: ./ops-tty-agent --port 80 --token mytoken
B（源节点）   : ./ops-tty-agent --port 8080 --shell bash --token mytoken
C（目标节点） : ./ops-tty-agent --port 8080 --shell bash --token mytoken
```

**请求：**
```bash
curl -X POST http://localhost:80/transfer \
  -H "Content-Type: application/json" \
  -H "X-Token: mytoken" \
  -d '{
    "source_url": "http://B:8080/download/data.tar.gz",
    "target_url": "http://C:8080/upload/data.tar.gz"
  }'
```

**参数说明：**
| 参数 | 说明 |
|------|------|
| `source_url` | 源文件下载地址（必须完整 URL） |
| `target_url` | 目标上传地址（必须完整 URL） |
| `source_token` | 源节点 Token（不填则用当前服务 Token） |
| `target_token` | 目标节点 Token（不填则用当前服务 Token） |

**响应：**
```json
{
  "success": true,
  "bytes": 104857600,
  "duration_ms": 15234
}
```

**特点：**
- **不落盘**：A 只中转，文件不落地 A 的磁盘
- **流式传输**：内存占用与文件大小无关，支持大文件
- **链式代理不支持**：`/transfer` 必须在 A（本机）直接调用，不能通过代理链转发

## Claude 使用建议

### 优先使用专用接口
Claude 在排查问题时，**优先使用专用接口**（`/tail`、`/stat`、`/disk`），而不是 `bash -c`：

| 场景 | 推荐接口 | 不推荐 |
|------|----------|--------|
| 看日志 | `/tail?path=/var/log/xxx` | `bash -c "tail -n 100 /var/log/xxx"` |
| 看文件大小/权限 | `/stat?path=/xxx` | `bash -c "ls -la /xxx"` |
| 看磁盘使用 | `/disk` | `bash -c "df -h"` |
| 执行复杂命令 | `/cmd` | — |

### 判断执行结果
调用 `/cmd` 后，按以下顺序判断：
1. `error != ""` → 系统错误（如超时），需要重试或检查环境
2. `exit_code != 0` → 命令执行失败，重点分析 `stderr`
3. `exit_code == 0` → 成功，分析 `stdout`
4. `truncated == true` → 输出被截断，需要缩小查询范围

## API 汇总

| 接口 | 方法 | 说明 |
|------|------|------|
| `/cmd` | POST | 执行命令（返回结构化 stdout/stderr/exit_code） |
| `/tail` | GET | 读取文件尾部（替代 `tail -n`，返回行数组） |
| `/stat` | GET | 查看文件信息（替代 `ls -la`，结构化返回） |
| `/disk` | GET | 查看磁盘使用（替代 `df -h`，结构化返回） |
| `/transfer` | POST | 跨节点文件传输（A 做中转，B→C） |
| `/upload/:filename` | PUT/POST | 上传文件 |
| `/download/:filename` | GET | 下载文件 |
| `/history` | GET | 查看本次执行历史 |
| `/history/:id` | GET | 查看单条历史详情 |
| `/history-files` | GET | 列出所有历史文件 |
| `/history-file/:filename` | GET | 读取指定历史文件 |

## 安全说明

### 自动确认模式
- `--auto-confirm no` (推荐)：每次执行命令都需要本地确认
  ```
  远程请求cmd: df -h
  是否同意执行 y/n: y
  cmd结果已成功返回!
  ```

- `--auto-confirm yes`：直接执行，无确认提示

### 支持的 Shell
- bash (默认)
- zsh
- sh

### 危险命令检查（可选）
```bash
# 启用危险命令检查
./ops-tty-agent --enable-block-check --block-commands "rm -rf /,dd if=,forkbomb"
```

## 使用场景

1. **远程运维** - 通过HTTP接口执行服务器命令
2. **文件传输** - 上传/下载脚本或配置文件
3. **批量操作** - 结合脚本批量执行命令
4. **跳板机代理** - 通过可访问节点代理访问隔离网络
5. **AI 辅助排查** - Claude 通过结构化接口高效获取系统状态
6. **跨机房文件传输** - 两个机房的服务器不互通，通过本机（A）做中转，把文件从 B 传到 C，不落地磁盘，支持大文件
