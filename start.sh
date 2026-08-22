#!/bin/bash
set -e
cd "$(dirname "$0")"

echo "🚀 正在编译、启动 Auto-Translator..."
pkill -f autotrans-web 2>/dev/null || true
pkill -f webrunner_test_bin 2>/dev/null || true # 清理测试遗留的孤儿服务

rm -f autotrans-web
go build -ldflags="-s -w" -o autotrans-web ./cmd/webrunner

# --- 本地推理引擎检测 (与页面自动检测一致) ---
ENGINES_FOUND=0
if (echo > /dev/tcp/127.0.0.1/8000) 2>/dev/null; then
    echo "✅ 检测到 oMLX 运行中 (127.0.0.1:8000 · 默认引擎)"
    ENGINES_FOUND=1
fi
if (echo > /dev/tcp/127.0.0.1/8080) 2>/dev/null; then
    echo "✅ 检测到 MLX server (127.0.0.1:8080)"
    ENGINES_FOUND=1
fi
if (echo > /dev/tcp/127.0.0.1/11434) 2>/dev/null; then
    echo "✅ 检测到 Ollama (127.0.0.1:11434)"
    ENGINES_FOUND=1
fi
if [ "$ENGINES_FOUND" = "0" ]; then
    echo "⚠️ 未检测到本地推理引擎 (8000/8080/11434 均未监听)，页面将无法列出模型"
    if command -v omlx >/dev/null 2>&1; then
        echo "   💡 可先运行: omlx start  (启动 oMLX 后刷新页面即可自动识别)"
    fi
fi

LOG_FILE="temp_uploads/autotrans-web.log"
PID_FILE="temp_uploads/autotrans-web.pid"
mkdir -p temp_uploads
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
        echo "🌐 服务地址: $URL (浏览器会自动打开)"
        exit 0
    fi
    sleep 0.2
done

echo "⚠️ 未在日志中找到服务地址，请打开日志查看: $LOG_FILE"
