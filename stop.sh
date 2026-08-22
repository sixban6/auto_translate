#!/bin/bash
cd "$(dirname "$0")"

echo "开始停止 autotrans-web..."
PID_FILE="temp_uploads/autotrans-web.pid"

# 定向停止：先清理该进程的 caffeinate 保活子进程，再停主进程
stop_pid() {
    local pid="$1"
    [ -z "$pid" ] && return 0
    if kill -0 "$pid" 2>/dev/null; then
        pkill -P "$pid" caffeinate 2>/dev/null || true
        kill "$pid" 2>/dev/null || true
    fi
}

if [ -f "$PID_FILE" ]; then
    stop_pid "$(cat "$PID_FILE")"
    rm -f "$PID_FILE"
fi

# 兜底：清理所有 autotrans-web 实例与测试遗留孤儿
for pid in $(pgrep -f autotrans-web 2>/dev/null); do
    stop_pid "$pid"
done
pkill -f webrunner_test_bin 2>/dev/null || true

echo "✅ 停止命令已执行"
