package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds the configuration for the auto-translation program.
type Config struct {
	APIURL            string            `json:"api_url"`             // e.g. "http://localhost:11434/v1/chat/completions"
	Model             string            `json:"model"`               // e.g. "qwen2.5:32b"
	Prompt            string            `json:"prompt"`              // System prompt
	Glossary          map[string]string `json:"glossary"`            // Dictionary of EN -> CN terms
	Concurrency       int               `json:"concurrency"`         // Number of concurrent translations, e.g. 2
	Temperature       float64           `json:"temperature"`         // Translation temperature, e.g. 0.1
	MaxChunkSize      int               `json:"max_chunk_size"`      // Max length of chunk text
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
	if cfg.Prompt == "" {
		return nil, fmt.Errorf("missing required field: prompt")
	}
	if cfg.InputFile == "" {
		return nil, fmt.Errorf("missing required field: input_file")
	}
	if cfg.OutputFile == "" {
		return nil, fmt.Errorf("missing required field: output_file")
	}

	// Set defaults if missing
	if cfg.Concurrency <= 0 {
		info, err := AutoCalculateConcurrency(cfg.APIURL, cfg.Model)
		if err != nil {
			// fallback
			cfg.Concurrency = 1
		} else {
			cfg.Concurrency = info.RecommendedC
			if info.WarningMsg != "" {
				cfg.SystemWarning = info.WarningMsg
			}
			cfg.SystemInfoMsg = fmt.Sprintf("[配置检测] 探测到物理内存 %dGB，模型估算基础占用 %dGB。 [智能规划] 建议并发=%d（安全系数已加入）。", info.TotalRAMBytes/(1024*1024*1024), info.ModelSize/(1024*1024*1024), info.RecommendedC)
		}
	}

	if cfg.Temperature <= 0 {
		cfg.Temperature = 0.1
	}
	if cfg.MaxChunkSize <= 0 {
		cfg.MaxChunkSize = 600
	}
	if cfg.RequestTimeoutSec <= 0 {
		cfg.RequestTimeoutSec = 180
	}

	return &cfg, nil
}
