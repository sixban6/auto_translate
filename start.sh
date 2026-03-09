#!/bin/bash
echo "🚀 正在编译、启动 Auto-Translator..."
pkill -f autotrans-web
pkill -f "cmd/webrunner/main.go"

rm -f autotrans-web
go build -ldflags="-s -w" -o autotrans-web ./cmd/webrunner/main.go

LOG_FILE="temp_uploads/autotrans-web.log"
PID_FILE="temp_uploads/autotrans-web.pid"
rm -f "$LOG_FILE" "$PID_FILE"
./autotrans-web > "$LOG_FILE" 2>&1 &
APP_PID=$!
echo "$APP_PID" > "$PID_FILE"

sleep 1

if ! kill -0 "$APP_PID" 2>/dev/null; then
    echo "❌ 启动失败，未找到进程。"
    echo "日志位置: $LOG_FILE"
    exit 1
fi

echo "✅ autotrans-web 启动成功，PID 为: $APP_PID"
for i in {1..20}; do
    URL=$(grep -Eo "http://localhost:[0-9]+" "$LOG_FILE" | tail -n 1)
    if [ -n "$URL" ]; then
        echo "🌐 服务地址: $URL"
#        if command -v open >/dev/null 2>&1; then
#            open "$URL"
#        fi
        exit 0
    fi
    sleep 0.2
done

echo "⚠️ 未在日志中找到服务地址，请打开日志查看: $LOG_FILE"
