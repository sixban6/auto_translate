# Auto-Translator

本项目是一个完全本地运行的翻译工具，支持 WebUI 交互式翻译与角色化 System Prompt 扩展。

## 特性

- **章节上下文批处理**：以章节为翻译上下文单位。同一章节的多个段落合并成一个批次一次请求，并携带章节标题与前文滚动上下文，翻译更连贯；请求次数大幅减少，速度显著提升。断点续传粒度为批次，旧版断点缓存仍可恢复。
- **三引擎支持 + 自动检测**：默认使用 **oMLX**（基于 MLX 的本地推理服务，自动读取 `~/.omlx/settings.json` 的 API key），也支持 **MLX**（`mlx_lm.server`）与 **Ollama**。打开页面即自动探测 `127.0.0.1:8000/8080/11434`，列出所有可用引擎的模型并自动选中。

## 一、启动与使用 WebUI 翻译

### 1. 环境要求

- Go 1.22+
- oMLX（默认引擎，Apple 芯片）、MLX 或 Ollama 任一

### 2. 启动推理引擎

#### 方式 A：oMLX（默认）

安装 oMLX 后（应用或 CLI 均可），启动托管服务：

```bash
omlx start
```

OpenAI 兼容地址为 `http://127.0.0.1:8000/v1`，API key 自动从 `~/.omlx/settings.json` 读取，无需任何配置。模型放在 `~/.omlx/models`（`omlx serve` 也可指定 `--model-dir`）。管理命令：`omlx stop` / `omlx restart` / `omlx diagnose`。

#### 方式 B：MLX（mlx_lm.server）

```bash
pip install mlx-lm
mlx_lm.server --model mlx-community/Qwen2.5-7B-Instruct-4bit
```

服务默认监听 `http://127.0.0.1:8080`。

#### 方式 C：Ollama

安装模型qwen3.5:9b
```bash
ollama pull qwen3.5:9b
```

以上引擎同时运行也没关系：WebUI 会同时列出所有引擎的模型（带引擎标注），选择某个模型会自动切换引擎与请求地址。

### 3. 启动 WebUI

在项目根目录执行：

```bash
chmod +x start.sh
bash start.sh
```

启动成功后, 会自动打开浏览器，显示翻译程序


https://github.com/user-attachments/assets/aeb2dca4-71ef-4cb4-8350-5de7282d7e75




停止程序
```bash
chmod +x start.sh
bash stop.sh
```

### 4. 本地编译与测试

```bash
bash build.sh            # 编译 autotrans-web(WebUI) + autotrans(CLI)
bash build.sh --test     # 先跑全量测试再编译
bash build.sh --race     # 关键并发用例 race 检测 + 编译
bash build.sh --all      # 测试 + race + 编译
bash build.sh --clean    # 清理旧二进制后重新编译
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
