# ops-tty-agent

[English](#english) | [中文](#中文)

---

<a name="中文"></a>

## 中文

命令行代理服务 - go-tty升级版，支持远程命令执行、文件上传/下载、多级跳转代理和Token认证。

## 功能特性

- 支持通过HTTP POST请求执行命令
- 支持文件上传/下载功能
- 支持多级跳转代理（A→B→C链式转发）
- 支持Token认证（指定或随机生成）
- 可配置端口、shell类型和是否自动确认执行命令
- 支持多架构编译（amd64、arm64）

## 安装

```bash
# 编译当前架构版本
go build -o ops-tty-agent .

# 编译多个架构版本
./build.sh
```

## 安装 Claude Code Skill

```bash
npx skills add https://github.com/xxwdll/ops-tty-agent --skill ops-tty-agent
```

安装后，Claude Code 会自动识别 `ops-tty-agent` 相关命令。

## 使用方法

### 启动服务

```bash
# 本地模式（执行本地命令）
./ops-tty-agent --port 80 --shell bash --auto-confirm no --token mytoken

# 代理模式（转发到目标节点）
./ops-tty-agent --port 80 --target http://b-node:8080 --token mytoken
```

### 命令行参数

| 参数 | 简写 | 说明 | 默认值 |
|------|------|------|--------|
| --port | -p | 服务端口 | 8080 |
| --shell | -s | shell类型 (bash/zsh/sh) - 仅本地模式有效 | bash |
| --auto-confirm | -a | 自动确认 (yes=不审计, no=需确认) - 仅本地模式有效 | no |
| --target | -t | 代理目标节点URL - 启用后进入代理模式 | 空 |
| --token | -k | 认证token - 不指定则随机生成 | 随机生成 |

### Token认证

所有请求必须在Header中携带 `X-Token`：

```bash
curl -H "X-Token: mytoken" http://localhost:80/cmd \
  -H "Content-Type: application/json" \
  -d '{"cmd":"ls"}'
```

- 指定 `--token`：使用固定token
- 不指定：启动时随机生成32位token，日志中显示

### 执行命令

```bash
curl -X POST http://localhost:80/cmd \
  -H "Content-Type: application/json" \
  -H "X-Token: mytoken" \
  -d '{"cmd":"df -h"}'
```

### 上传文件

```bash
curl -X PUT http://localhost:80/upload/test.txt \
  -H "X-Token: mytoken" \
  --data-binary @test.txt
```

### 下载文件

```bash
curl -X GET http://localhost:80/download/test.txt \
  -H "X-Token: mytoken" \
  -o test.txt
```

### 代理模式

当你只能访问A节点，但需要操作B节点时：

```bash
# A节点（代理模式）
./ops-tty-agent --port 80 --target http://b-node:8080 --token chain-token

# B节点（本地模式）
./ops-tty-agent --port 8080 --shell bash --token chain-token
```

请求发往A节点，自动转发到B节点执行。链路中所有节点需使用相同Token。

## 注意事项

- 本工具可以执行任意命令，请注意安全使用
- 建议在受控环境中使用，并设置强Token
- 上传的文件会保存在当前目录的 `uploads` 文件夹中
- 代理模式下 `--shell` 和 `--auto-confirm` 参数无效

---

<a name="english"></a>

## English

Command-line proxy service - an upgraded version of go-tty, supporting remote command execution, file upload/download, multi-hop proxy, and token authentication.

## Features

- Execute commands via HTTP POST requests
- File upload/download support
- Multi-hop proxy support (A→B→C chain forwarding)
- Token authentication (specified or auto-generated)
- Configurable port, shell type, and auto-confirmation
- Multi-architecture builds (amd64, arm64)

## Installation

```bash
# Build for current architecture
go build -o ops-tty-agent .

# Build for multiple architectures
./build.sh
```

## Install Claude Code Skill

```bash
npx skills add https://github.com/xxwdll/ops-tty-agent --skill ops-tty-agent
```

After installation, Claude Code will automatically recognize `ops-tty-agent` related commands.

## Usage

### Start Service

```bash
# Local mode (execute commands locally)
./ops-tty-agent --port 80 --shell bash --auto-confirm no --token mytoken

# Proxy mode (forward to target node)
./ops-tty-agent --port 80 --target http://b-node:8080 --token mytoken
```

### Command Line Arguments

| Argument | Short | Description | Default |
|----------|-------|-------------|---------|
| --port | -p | Service port | 8080 |
| --shell | -s | Shell type (bash/zsh/sh) - local mode only | bash |
| --auto-confirm | -a | Auto-confirm (yes/no) - local mode only | no |
| --target | -t | Target node URL - enables proxy mode | empty |
| --token | -k | Auth token - auto-generated if not specified | random |

### Token Authentication

All requests must include `X-Token` in the header:

```bash
curl -H "X-Token: mytoken" http://localhost:80/cmd \
  -H "Content-Type: application/json" \
  -d '{"cmd":"ls"}'
```

- With `--token`: Use specified token
- Without: Auto-generate 32-char token, shown in logs

### Execute Command

```bash
curl -X POST http://localhost:80/cmd \
  -H "Content-Type: application/json" \
  -H "X-Token: mytoken" \
  -d '{"cmd":"df -h"}'
```

### Upload File

```bash
curl -X PUT http://localhost:80/upload/test.txt \
  -H "X-Token: mytoken" \
  --data-binary @test.txt
```

### Download File

```bash
curl -X GET http://localhost:80/download/test.txt \
  -H "X-Token: mytoken" \
  -o test.txt
```

### Proxy Mode

When you can only access Node A but need to operate Node B:

```bash
# Node A (proxy mode)
./ops-tty-agent --port 80 --target http://b-node:8080 --token chain-token

# Node B (local mode)
./ops-tty-agent --port 8080 --shell bash --token chain-token
```

Requests sent to Node A are automatically forwarded to Node B. All nodes in the chain must use the same token.

## Notes

- This tool can execute arbitrary commands - use with caution
- Recommended to use in controlled environments with strong tokens
- Uploaded files are saved in the `uploads` directory
- In proxy mode, `--shell` and `--auto-confirm` are ignored
