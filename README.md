# Auto-Translator CLI

这是一个完全本地运行、轻量级、**零外部依赖**（除 Go 标准解析库以外）的并发翻译命令行工具。它复刻了广受好评的“沉浸式翻译(Immersive Translate)”的工作流，完美对接本地 LLM 服务器（例如 Ollama）。

目前支持 **`.epub`** 和 **`.txt`** 格式的文件解析与翻译。

## 核心特性

- **零 API 成本**：完美对接你的本地 `Ollama` OpenAI 兼容接口，所有数据都在本地处理。
- **防止 OOM 的智能并发**：通过 Go 的 Worker-Pool 实现稳健的并发请求速率限制，保护你的本地显卡不会因为瞬间并发过大而显存爆炸。
- **无损 Epub 排版**：通过解析电子书的底层 HTML 节点，它只提取并翻译纯文本，随后将译文精准注回抽象语法树（AST 中）。100% 保留原有排版结构、CSS 样式与图片。
- **智能长文本断句**：自动应对超长段落。具有多级回退策略（段落 -> 句号 -> 定长切割），确保无论多长的文本都能被完美分块，翻译后无缝粘合。
- **术语字典强制校准 (Glossary)**：即便本地大模型由于智力限制没有遵守 Prompt，工具也会在后处理阶段基于你配置的专业词典强行纠正关键术语的翻译。
- **输出格式高强度净化**：使用多重正则表达式与字符串清洗，彻底消灭本地 LLM 喜欢自作主张输出的 `"Here is the translation:"` 和 Markdown 代码块 (```) 噪音。
- **中英双语对照输出**：只需在配置里开启 `bilingual: true`，即可一键生成排版优美的中英双语版电子书或 TXT 文件。

---

## 快速开始

### 1. 环境依赖

- [Go](https://go.dev/doc/install) 环境 (1.22 及以上版本)
- 一个正在运行的本地/远程 OpenAI 兼容接口（推荐使用 [Ollama](https://ollama.com/) 并在本地跑起类似 `qwen2.5:32b` 的大模型）。

### 2. 准备配置文件

在任意位置创建一个 `config.json` 文件（项目代码中已附带示例配置）：

```json
{
  "api_url": "http://localhost:11434/v1/chat/completions",
  "model": "qwen2.5:32b",
  "prompt": "You are a professional financial translator specializing in VSA (Volume Spread Analysis) and Wyckoff Theory.\nTranslate the given text into concise, professional Chinese suitable for senior traders.\nCRITICAL: Output ONLY the translated Chinese text. No markdown, no explanations, no original text.",
  "glossary": {
    "Ventas": "抛压",
    "Selling": "抛压",
    "Compras": "买盘",
    "Buying": "买盘",
    "El camino de menor resistencia": "阻力最小路径",
    "Supply": "供应",
    "Demand": "需求"
  },
  "concurrency": 2,
  "temperature": 0.1,
  "max_chunk_size": 2000,
  "request_timeout_sec": 300,
  "input_file": "./input.txt",
  "output_file": "./output.txt",
  "bilingual": true
}
```

#### 配置项说明

*   `api_url` (必填): 你的 OpenAI 兼容服务端点。
*   `model` (必填): 你希望使用的具体模型名称。
*   `prompt` (必填): System Prompt。明确告诉 AI 它需要承担的角色并禁止输出除译文外的任何多余字符。
*   `input_file` (必填): 需要翻译的输入文件（支持 `.txt` 或 `.epub`）。
*   `output_file` (必填): 生成的对照文档/双语电子书保存路径。
*   `glossary` (选填): 字典。英文单词与其应被翻译成的中文术语之间的映射。
*   `concurrency` (选填，默认 2): 并发翻译数。请务必根据你的 GPU 显存大小严格限制此项，以防请求堆叠导致崩溃。
*   `bilingual` (选填，默认 false): 开启后，译文内容将与源文本交替输出（中英对照模式）。

### 3. 一键运行

在项目根目录下，指定配置文件的路径并执行 CLI：

```bash
go run ./cmd/autotrans -c config.json
```

或者，你也可以将其编译为可执行文件再运行：

```bash
go build -o autotrans ./cmd/autotrans
./autotrans -c config.json
```

### 4. （全新！）使用沉浸式交互 Web 界面

为了提供无可挑剔的用户体验，项目中包含了一个非常精美且功能完备的纯本地 Web 服务器 (`webrunner`)。它无需复杂的桌面环境，只需运行命令，你就能在浏览器中直观地看到进度条、调整参数并拖拽上传文档！

在项目目录执行：
```bash
go run ./cmd/webrunner/main.go
# 或者编译执行: go build -o autotrans_web ./cmd/webrunner && ./autotrans_web
```

然后打开你的浏览器，访问： [http://localhost:4000](http://localhost:4000)

### 5. 运行 TDD 测试单元

本项目采用了完整的测试驱动开发（TDD）覆盖，包含边界拆分验证及完整的终端 HTTP Mock 测试。你可以通过以下命令验证全流程有效性：

```bash
go test -v ./...
```

## 开源许可 (License)

本项目采用 [MIT License](LICENSE) 授权开源体系。
