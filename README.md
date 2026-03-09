# Auto-Translator

本项目是一个完全本地运行的翻译工具，支持 WebUI 交互式翻译与角色化 System Prompt 扩展。

## 一、启动与使用 WebUI 翻译

### 1. 环境要求

- Go 1.22+
- Ollama

### 2. 启动 WebUI

安装模型qwen3.5:9b
```bash
ollama pull qwen3.5:9b
```

在项目根目录执行：

```bash
chmod +x start.sh
bash start.sh
```

启动成功后, 会自动打开浏览器，显示翻译程序

<video src="./auto-trans.mp4" controls="controls" width="100%" muted="muted"></video>

停止程序
```bash
chmod +x start.sh
bash stop.sh
```

## 二、扩展翻译角色

系统会自动加载 prompts 目录下的所有 Markdown 文件作为“翻译专家”角色，文件名即为角色名称。

### 新增角色步骤

1. 在 prompts 目录新建 Markdown 文件，例如：

```
prompts/新能源翻译专家.md
```

2. 在文件中写入角色的 System Prompt 内容，例如：

```
你是一位新能源行业资深译者，熟悉电池、电驱与储能领域术语。
请将输入文本翻译为准确、简洁、符合工程师阅读习惯的中文。
仅输出翻译结果，不要包含原文或说明。
```

3. 刷新 WebUI，即可在角色下拉菜单中看到新角色