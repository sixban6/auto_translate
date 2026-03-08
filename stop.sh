#!/bin/bash
echo "开始停止 autotrans-web"
if ! pkill autotrans-web 2>/dev/null; then
  echo "✅ 已经停止"
else
  echo "✅ autotrans-web 停止成功"
fi