#!/bin/bash
echo "🚀 正在编译、启动 Auto-Translator..."
OLD_PIDS=$(pgrep -x autotrans-web)
if [ -n "$OLD_PIDS" ]; then
    echo "🧹 检测到旧进程，正在停止: $OLD_PIDS"
    kill $OLD_PIDS
    sleep 1
fi

rm -f autotrans-web
go build -ldflags="-s -w" -o autotrans-web ./cmd/webrunner/main.go
./autotrans-web &
APP_PID=$!

sleep 1

if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "❌ 启动失败，未找到进程。"
else
    echo "✅ autotrans-web 启动成功，PID 为: $APP_PID"
fi
