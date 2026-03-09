#!/bin/bash
echo "开始停止 autotrans-web"
pkill -f caffeinate
PID_FILE="temp_uploads/autotrans-web.pid"
if [ -f "$PID_FILE" ]; then
  PID=$(cat "$PID_FILE")
  if kill -0 "$PID" 2>/dev/null; then
    kill "$PID"
  fi
  rm -f "$PID_FILE"
fi
pkill -f autotrans-web 2>/dev/null
pkill -f "cmd/webrunner/main.go" 2>/dev/null
PORT_PIDS=$(lsof -tiTCP:4000 -sTCP:LISTEN 2>/dev/null)
if [ -n "$PORT_PIDS" ]; then
  kill $PORT_PIDS 2>/dev/null
fi
echo "✅ 停止命令已执行"
