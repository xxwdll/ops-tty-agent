# ops-tty-agent

命令行代理服务 - go-tty升级版，可以在本地代理命令行进行，有CLI交互。

## 功能特性

- 支持通过HTTP POST请求执行命令
- 支持文件上传功能
- 可配置端口、shell类型和是否自动确认执行命令
- 支持多架构编译（amd64、arm64）

## 安装

### 编译安装

```bash
# 编译当前架构版本
go build -o ops-tty-agent .

# 编译多个架构版本
./build.sh
```

## 使用方法

### 启动服务

```bash
# 基本用法
./ops-tty-agent --port 80 --shell bash --auto-confirm no

# 自动确认执行命令
./ops-tty-agent --port 80 --shell bash --auto-confirm yes
```

### 执行命令

```bash
curl -X POST -H "Content-Type: application/json" -d '{"cmd": "df -h"}' http://<主机地址>:80/cmd
```

### 上传文件

```bash
curl --upload-file text.txt http://<主机地址>:80/upload
```

## 命令行参数

- `--port`, `-p`: 服务端口（默认：8080）
- `--shell`, `-s`: 使用的shell类型（默认：bash）
- `--auto-confirm`, `-a`: 是否自动确认执行命令（默认：no）

## 示例

### 示例1：启动服务并手动确认命令执行

```bash
./ops-tty-agent --port 80 --shell bash --auto-confirm no
```

远程请求：

```bash
curl -X POST -H "Content-Type: application/json" -d '{"cmd": "df -h"}' http://localhost:80/cmd
```

服务端会提示：

```
远程请求cmd: df -h
是否同意执行 y/n: y
cmd结果已成功返回!
```

### 示例2：启动服务并自动确认命令执行

```bash
./ops-tty-agent --port 80 --shell bash --auto-confirm yes
```

远程请求：

```bash
curl -X POST -H "Content-Type: application/json" -d '{"cmd": "df -h"}' http://localhost:80/cmd
```

服务端会直接执行命令并返回结果，无需用户确认。

## 注意事项

- 本工具可以执行任意命令，请注意安全使用
- 建议在受控环境中使用，并设置强密码或其他安全措施
- 上传的文件会保存在当前目录的`uploads`文件夹中