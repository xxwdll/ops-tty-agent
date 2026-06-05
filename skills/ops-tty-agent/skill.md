---
name: ops-tty-agent
description: 命令行代理服务 - 在本地代理远程命令执行和文件上传/下载，支持多级跳转代理和Token认证。当用户提及"ops-tty"、"代理命令"、"远程执行cmd"、"上传文件"、"下载文件"、"跳转代理"、"执行历史"时使用此技能。
version: 1.3.0
---

# ops-tty-agent - 命令行代理服务

ops-tty-agent 是 go-tty 项目的升级版，用于在本地代理命令行执行，支持远程命令执行、文件上传/下载、多级跳转代理和执行历史记录。

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

## 执行历史记录

每次启动会生成一个带时间戳的历史文件，记录所有执行的命令和输出。Claude Code 可以通过这些历史了解之前做了什么。

### 历史文件命名规则
```
ops-history_YYYY-MM-DD_HH-MM-SS.json
```

例如：`ops-history_2026-05-29_22-24-23.json`

### 查看所有历史文件
```bash
curl -H "X-Token: your-token" http://localhost:80/history-files
```

**响应：**
```json
[
  {
    "filename": "ops-history_2026-05-29_22-24-23.json",
    "start_time": "2026-05-29 22-24-23",
    "records": 5
  },
  {
    "filename": "ops-history_2026-05-28_10-00-00.json",
    "start_time": "2026-05-28 10-00-00",
    "records": 3
  }
]
```

### 查看本次执行历史
```bash
curl -H "X-Token: your-token" http://localhost:80/history
```

**响应：**
```json
[
  {
    "id": 1,
    "timestamp": "2026-05-29T22:20:35.794882+08:00",
    "cmd": "df -h",
    "output": "Filesystem 100G...",
    "success": true,
    "duration_ms": 5
  }
]
```

### 查看指定历史文件
```bash
curl -H "X-Token: your-token" http://localhost:80/history-file/ops-history_2026-05-28_10-00-00.json
```

### 使用场景
机器开机后，Claude Code 可以：
1. 调用 `/history-files` 查看所有历史文件
2. 通过文件名知道每次启动的时间
3. 调用 `/history-file/xxx` 读取特定历史，了解之前做了什么

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
{"output":"Filesystem      Size  Used Avail Use% Mounted on\n..."}
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

## API 汇总

| 接口 | 方法 | 说明 |
|------|------|------|
| `/cmd` | POST | 执行命令 |
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
5. **执行历史追踪** - Claude Code 可查看历史了解之前做了什么
