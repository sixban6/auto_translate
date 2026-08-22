#!/bin/bash
# Auto-Translator 本地编译/测试脚本
#
# 用法:
#   bash build.sh            编译 webrunner(autotrans-web) + CLI(autotrans)
#   bash build.sh --test     编译前先跑全量测试 (go test ./...)
#   bash build.sh --race     对关键并发用例跑 race 检测
#   bash build.sh --clean    清理旧二进制与测试产物后再编译
#   bash build.sh --all      --test + --race + 编译

set -e
cd "$(dirname "$0")"

RUN_TEST=0
RUN_RACE=0
RUN_CLEAN=0
for arg in "$@"; do
  case "$arg" in
    --test)  RUN_TEST=1 ;;
    --race)  RUN_RACE=1 ;;
    --clean) RUN_CLEAN=1 ;;
    --all)   RUN_TEST=1; RUN_RACE=1 ;;
    *) echo "未知参数: $arg (支持 --test --race --clean --all)"; exit 1 ;;
  esac
done

if [ "$RUN_CLEAN" = "1" ]; then
  echo "🧹 清理旧产物..."
  rm -rf autotrans-web autotrans autotrans_test_bin
fi

echo "🔍 go vet..."
go vet ./...

if [ "$RUN_TEST" = "1" ]; then
  echo "🧪 全量测试 (go test ./...)..."
  go test ./... -count=1
fi

if [ "$RUN_RACE" = "1" ]; then
  echo "🏃 并发 race 检测 (处理器/章节批处理/任务删除用例)..."
  go test -race ./test/ -count=1 \
    -run 'TestProcessor$|TestProcessorChapterContextBatches|TestProcessorNewFormatResume|TestTaskDelete_CancelRunningTaskFirst'
fi

echo "📦 编译 webrunner (autotrans-web)..."
go build -ldflags="-s -w" -o autotrans-web ./cmd/webrunner

echo "📦 编译 CLI (autotrans)..."
go build -ldflags="-s -w" -o autotrans ./cmd/autotrans

echo ""
echo "✅ 编译完成:"
ls -lh autotrans-web autotrans | awk '{print "   " $9 " (" $5 ")"}'
echo ""
echo "   启动 WebUI:  bash start.sh   (或直接 ./autotrans-web)"
echo "   CLI 用法:    ./autotrans -c config.json"
