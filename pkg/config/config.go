package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config holds the configuration for the auto-translation program.
type Config struct {
	APIURL            string            `json:"api_url"`             // e.g. "http://localhost:11434/v1/chat/completions"
	Model             string            `json:"model"`               // e.g. "qwen2.5:32b"
	Prompt            string            `json:"prompt"`              // System prompt
	PromptRole        string            `json:"prompt_role"`         // Role name for system prompt
	Glossary          map[string]string `json:"glossary"`            // Dictionary of EN -> CN terms
	Concurrency       int               `json:"concurrency"`         // Number of concurrent translations, e.g. 2
	Temperature       float64           `json:"temperature"`         // Translation temperature, e.g. 0.1
	MaxChunkSize      int               `json:"max_chunk_size"`      // Max length of chunk text
	MaxRetries        int               `json:"max_retries"`         // Max retry attempts per chunk
	RequestTimeoutSec int               `json:"request_timeout_sec"` // HTTP timeout in seconds
	InputFile         string            `json:"input_file"`          // Path to input file (.txt, .epub)
	OutputFile        string            `json:"output_file"`         // Path to save output file
	Bilingual         bool              `json:"bilingual"`           // Output bilingual format if true
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
	cpuCap := maxConcurrencyByCPU()
	modelCap := maxConcurrencyByModel(cfg.Model)
	if cfg.Concurrency <= 0 {
		info, err := AutoCalculateConcurrency(cfg.APIURL, cfg.Model)
		if err != nil {
			// fallback
			cfg.Concurrency = 1
			cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 未知配置。建议并发数：%d。", cfg.Concurrency)
		} else {
			cfg.Concurrency = info.RecommendedC
			if info.WarningMsg != "" {
				cfg.SystemWarning = info.WarningMsg
			}
			cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 探测到物理内存 %dGB，模型估算基础占用 %dGB。 [智能规划] 建议并发数：%d（安全系数已加入）。", info.TotalRAMBytes/(1024*1024*1024), info.ModelSize/(1024*1024*1024), info.RecommendedC)
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
		cfg.RequestTimeoutSec = 180
	}
}

// GetConfigExplanation generates a human-readable explanation of the current translation strategy.
func GetConfigExplanation(cfg *Config) string {
	var sb strings.Builder
	sb.WriteString(cfg.SystemInfoMsg)
	sb.WriteString(fmt.Sprintf("\n[当前策略] 模型=%s | 块大小=%d | 重试=%d | 超时=%ds", cfg.Model, cfg.MaxChunkSize, cfg.MaxRetries, cfg.RequestTimeoutSec))
	if cfg.SystemWarning != "" {
		sb.WriteString("\n[运行警告] " + cfg.SystemWarning)
	}
	return sb.String()
}

func AutoCalculateMaxChunkSize(modelName string) int {
	model := strings.ToLower(modelName)
	size := 800
	switch {
	case strings.Contains(model, "qwen2.5"):
		size = 1100
	case strings.Contains(model, "qwen3.5"):
		size = 700
	case strings.Contains(model, "llama3"):
		size = 1100
	}
	if size < 400 {
		return 400
	}
	if size > 1400 {
		return 1400
	}
	return size
}

func maxConcurrencyByModel(modelName string) int {
	//model := strings.ToLower(modelName)
	//if strings.Contains(model, "translategemma") {
	//	return 5
	//}
	//if strings.Contains(model, "qwen") || strings.Contains(model, "deepseek") || strings.Contains(model, "llama") || strings.Contains(model, "mistral") {
	//	return 5
	//}
	return 5
}
