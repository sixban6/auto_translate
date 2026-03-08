#!/bin/bash
echo "🚀 正在编译、启动 Auto-Translator..."
rm -rf autotrans-web
go build -ldflags="-s -w" -o autotrans-web ./cmd/webrunner/main.go
./autotrans-web &

# 等待 1 秒确保进程完全启动
sleep 1

# 捕获 PID
PID=$(ps -ef | grep autotrans-web | grep -v grep | awk '{print $2}')

if [ -z "$PID" ]; then
    echo "❌ 启动失败，未找到进程。"
else
    echo "✅ autotrans-web 启动成功，PID 为: $PID"
    # 如果你想在这里加提示音
    # say "service started"
fi
