#!/bin/bash

# 编译多个架构版本的ops-tty-agent

echo "开始编译多个架构版本..."

# 创建输出目录
mkdir -p builds

# 编译amd64架构版本
echo "编译 amd64 架构版本..."
GOOS=linux GOARCH=amd64 go build -o builds/ops-tty-agent-linux-amd64 .

# 编译arm64架构版本
echo "编译 arm64 架构版本..."
GOOS=linux GOARCH=arm64 go build -o builds/ops-tty-agent-linux-arm64 .

# 编译macOS amd64架构版本
echo "编译 macOS amd64 架构版本..."
GOOS=darwin GOARCH=amd64 go build -o builds/ops-tty-agent-darwin-amd64 .

# 编译macOS arm64架构版本
echo "编译 macOS arm64 架构版本..."
GOOS=darwin GOARCH=arm64 go build -o builds/ops-tty-agent-darwin-arm64 .

# 编译Windows amd64架构版本
echo "编译 Windows amd64 架构版本..."
GOOS=windows GOARCH=amd64 go build -o builds/ops-tty-agent-windows-amd64.exe .

echo "编译完成！可执行文件位于 builds 目录中。"

# 显示编译结果
ls -la builds/
