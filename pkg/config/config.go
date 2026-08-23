package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Engine identifiers. EngineOmlx is the oMLX server (https://github.com
// sst-omlx/omlx style managed MLX server, OpenAI-compatible with API key
// auth). EngineMLX covers any other OpenAI-compatible chat server
// (mlx_lm.server, LM Studio, remote OpenAI-style APIs).
const (
	EngineOllama = "ollama"
	EngineMLX    = "mlx"
	EngineOmlx   = "omlx"
)

// Default engine endpoints.
const (
	// DefaultOMLXEndpoint is the oMLX OpenAI-compatible base URL.
	DefaultOMLXEndpoint = "http://127.0.0.1:8000/v1"
	// DefaultOMLXChatEndpoint is the full chat completions path of oMLX.
	DefaultOMLXChatEndpoint = "http://127.0.0.1:8000/v1/chat/completions"
	DefaultMLXEndpoint      = "http://127.0.0.1:8080/v1/chat/completions"
)

// OMLXSettingsPath returns the oMLX settings file location (~/.omlx/settings.json).
func OMLXSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".omlx", "settings.json")
}

// ReadOMLXAPIKey extracts auth.api_key from the local oMLX settings file.
// Returns "" when oMLX is not installed or has no key configured.
func ReadOMLXAPIKey() string {
	path := OMLXSettingsPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var settings struct {
		Auth struct {
			APIKey string `json:"api_key"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	return strings.TrimSpace(settings.Auth.APIKey)
}

// ResolveEngine decides which request protocol to use for a config.
// Explicit engine wins; otherwise it is inferred from the API URL and the
// model naming style. Default is EngineOmlx (the local oMLX server).
func ResolveEngine(apiURL, model, engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case EngineOllama:
		return EngineOllama
	case EngineOmlx:
		return EngineOmlx
	case EngineMLX, "openai", "openai-compatible", "lmstudio":
		return EngineMLX
	}
	if strings.Contains(strings.ToLower(model), "translategemma") {
		// Legacy special case: posts OpenAI-style payloads to api_url as-is.
		return EngineMLX
	}
	url := strings.ToLower(apiURL)
	if strings.Contains(url, ":11434") || strings.Contains(url, "/api/chat") ||
		strings.Contains(url, "/api/generate") || strings.Contains(url, "/api/tags") {
		return EngineOllama
	}
	if strings.Contains(url, ":8000") || strings.Contains(url, "127.0.0.1/omlx") {
		return EngineOmlx
	}
	if strings.Contains(model, ":") && !strings.Contains(model, "/") {
		// Ollama naming style like "qwen2.5:14b".
		return EngineOllama
	}
	if strings.Contains(model, "/") {
		// HF-path style model ids are served by mlx_lm.server / LM Studio.
		return EngineMLX
	}
	return EngineOmlx
}

// Config holds the configuration for the auto-translation program.
type Config struct {
	APIURL            string            `json:"api_url"`             // e.g. "http://127.0.0.1:8000/v1/chat/completions" (oMLX)
	Engine            string            `json:"engine"`              // "omlx" (default), "mlx" or "ollama"
	Model             string            `json:"model"`               // e.g. "Qwen3.8-27B-oQ4e-mtp"
	APIKey            string            `json:"api_key"`             // Bearer token for the engine (oMLX reads ~/.omlx/settings.json when empty)
	Prompt            string            `json:"prompt"`              // System prompt
	PromptRole        string            `json:"prompt_role"`         // Role name for system prompt
	Glossary          map[string]string `json:"glossary"`            // Dictionary of EN -> CN terms
	Concurrency       int               `json:"concurrency"`         // Number of concurrent translations, e.g. 2
	Temperature       float64           `json:"temperature"`         // Translation temperature, e.g. 0.1
	MaxChunkSize      int               `json:"max_chunk_size"`      // Max length of one chapter batch (runes)
	MaxRetries        int               `json:"max_retries"`         // Max retry attempts per chunk
	RequestTimeoutSec int               `json:"request_timeout_sec"` // HTTP timeout in seconds
	InputFile         string            `json:"input_file"`          // Path to input file (.txt, .epub)
	OutputFile        string            `json:"output_file"`         // Path to save output file
	Bilingual         bool              `json:"bilingual"`           // Output bilingual format if true
	// ChapterBatching enables the chapter-aware pipeline: paragraphs of
	// the same chapter are packed into batches and translated with the
	// chapter title plus rolling previous-tail context. When false (the
	// default), every paragraph is translated independently — the classic
	// per-block mode — with state keys identical to the pre-chapter
	// version, so old checkpoints resume seamlessly.
	ChapterBatching   bool              `json:"chapter_batching"`
	SystemWarning     string            `json:"-"`                   // Runtime hardware warning
	SystemInfoMsg     string            `json:"-"`                   // Runtime hardware info
}

// Load loads the configuration from a JSON file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Validation
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("missing required field: api_url")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("missing required field: model")
	}
	if cfg.Prompt == "" && cfg.PromptRole == "" {
		return nil, fmt.Errorf("missing required field: prompt or prompt_role")
	}
	if cfg.InputFile == "" {
		return nil, fmt.Errorf("missing required field: input_file")
	}
	if cfg.OutputFile == "" {
		return nil, fmt.Errorf("missing required field: output_file")
	}

	// Set defaults if missing
	cfg.AutoDetectAndCalculate()

	return &cfg, nil
}

// AutoDetectAndCalculate populates the config with default calculated values based on current environment and model.
func (cfg *Config) AutoDetectAndCalculate() {
	// Normalize the engine (explicit value wins, otherwise inferred from
	// URL/model style; defaults to MLX / OpenAI-compatible).
	cfg.Engine = ResolveEngine(cfg.APIURL, cfg.Model, cfg.Engine)

	cpuCap := maxConcurrencyByCPU()
	modelCap := maxConcurrencyByModel(cfg.Model)
	if cfg.Concurrency <= 0 {
		info, err := AutoCalculateConcurrency(cfg.APIURL, cfg.Model, cfg.Engine)
		if err != nil {
			// fallback
			cfg.Concurrency = 1
			cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 未知配置。建议并发数：%d。", cfg.Concurrency)
		} else {
			cfg.Concurrency = info.RecommendedC
			if info.WarningMsg != "" {
				cfg.SystemWarning = info.WarningMsg
			}
			cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 探测到物理内存 %dGB，模型「%s」预估基础占用 %dGB（引擎=%s，章节上下文批处理已启用）。 [智能规划] 建议并发数：%d（安全系数已加入）。", info.TotalRAMBytes/(1024*1024*1024), cfg.Model, info.ModelSize/(1024*1024*1024), EngineLabel(cfg.Engine), info.RecommendedC)
		}
	} else if cfg.SystemInfoMsg == "" {
		cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 用户指定并发数：%d。", cfg.Concurrency)
	}
	if cfg.Concurrency > cpuCap {
		cfg.Concurrency = cpuCap
		if cfg.SystemWarning != "" {
			cfg.SystemWarning += " "
		}
		cfg.SystemWarning += fmt.Sprintf("⚠️ 并发上限已按 CPU 核心约束为 %d（核心数-1）。", cpuCap)
	}
	if cfg.Concurrency > modelCap {
		cfg.Concurrency = modelCap
		if cfg.SystemWarning != "" {
			cfg.SystemWarning += " "
		}
		cfg.SystemWarning += fmt.Sprintf("⚠️ 当前模型已启用稳态并发上限 %d，以降低排队超时。", modelCap)
	}

	if cfg.Temperature <= 0 {
		cfg.Temperature = 0.1
	}
	if cfg.MaxChunkSize <= 0 {
		cfg.MaxChunkSize = AutoCalculateMaxChunkSize(cfg.Model)
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 5
	}
	if cfg.RequestTimeoutSec <= 0 {
		cfg.RequestTimeoutSec = 300
	}
}

// EngineLabel returns a human-readable engine name for logs and the UI.
func EngineLabel(engine string) string {
	switch engine {
	case EngineOllama:
		return "Ollama"
	case EngineMLX:
		return "MLX"
	case EngineOmlx:
		return "oMLX"
	}
	return engine
}

// GetConfigExplanation generates a human-readable explanation of the current translation strategy.
func GetConfigExplanation(cfg *Config) string {
	var sb strings.Builder
	sb.WriteString(cfg.SystemInfoMsg)
	sb.WriteString(fmt.Sprintf("\n[当前策略] 引擎=%s | 模型=%s | 模式=%s | 章节批次大小=%d | 重试=%d | 超时=%ds", EngineLabel(cfg.Engine), cfg.Model, modeLabel(cfg), cfg.MaxChunkSize, cfg.MaxRetries, cfg.RequestTimeoutSec))
	if cfg.SystemWarning != "" {
		sb.WriteString("\n[运行警告] " + cfg.SystemWarning)
	}
	return sb.String()
}

// modeLabel describes the batching strategy in user-facing messages.
func modeLabel(cfg *Config) string {
	if cfg.ChapterBatching {
		return "章节批处理"
	}
	return "逐段直译"
}

// ModeLabel is the exported form of modeLabel for other packages (logs,
// resume notices).
func ModeLabel(cfg *Config) string { return modeLabel(cfg) }

// modelParamCountGB extracts the parameter count ("7B", "27B", "0.5B"...)
// from a model name; 0 when absent.
func modelParamCount(model string) float64 {
	for _, m := range paramCountPattern.FindAllStringSubmatch(strings.ToLower(model), -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err == nil && v > 0 {
			return v
		}
	}
	return 0
}

var paramCountPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*b`)

// AutoCalculateMaxChunkSize picks the chapter-batch target (runes) per model.
// Translation output length ≈ input length, so the batch size is bound by the
// model's discipline over long generations more than by its context window:
// small models drift/echo on very long outputs, so they get smaller batches;
// large instruct models (>=14B) hold structure reliably and get bigger ones.
func AutoCalculateMaxChunkSize(modelName string) int {
	model := strings.ToLower(modelName)
	if strings.Contains(model, "translategemma") {
		return 800
	}
	if params := modelParamCount(model); params >= 14 {
		return 3200
	}
	return 2400
}
