package config

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SystemInfo holds detected hardware information and the calculation result
type SystemInfo struct {
	TotalRAMBytes uint64
	ModelSize     uint64
	RecommendedC  int
	WarningMsg    string // Warning to show to user if they need to export OLLAMA_NUM_PARALLEL
}

// getSystemRAMBytes attempts to get the total physical memory of the system in bytes.
func getSystemRAMBytes() (uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("sysctl", "-n", "hw.memsize")
		out, err := cmd.Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	case "linux":
		cmd := exec.Command("awk", "/MemTotal/ {print $2}", "/proc/meminfo")
		out, err := cmd.Output()
		if err != nil {
			return 0, err
		}
		kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_ComputerSystem | Select-Object -ExpandProperty TotalPhysicalMemory")
		out, err := cmd.Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	default:
		return 0, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// getModelSizeBytes asks the local Ollama API for the size of the target model in bytes
func getModelSizeBytes(apiURL string, targetModel string) (uint64, error) {
	baseURL := "http://localhost:11434"

	// 【修复 3】使用标准库解析 URL，避免字符串截取导致的越界或格式错误
	if apiURL != "" {
		if !strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://") {
			apiURL = "http://" + apiURL
		}
		if parsed, err := url.Parse(apiURL); err == nil {
			baseURL = parsed.Scheme + "://" + parsed.Host
		}
	}

	tagsURL := baseURL + "/api/tags"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(tagsURL)
	if err != nil {
		return 0, fmt.Errorf("failed to reach Ollama tags api: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("bad status from %s: %d", tagsURL, resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Models []struct {
			Name string `json:"name"`
			Size uint64 `json:"size"`
		} `json:"models"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	// 【修复你发现的 Bug】：拆分为两阶段匹配，确保模型大小获取精准无误

	// 第一阶段：完全精确匹配 (例如用户输入了 qwen3.5:9b)
	for _, m := range result.Models {
		if m.Name == targetModel {
			return m.Size, nil
		}
	}

	// 第二阶段：前缀模糊匹配 (仅当 targetModel 没有带 ":" 标签时才允许)
	targetParts := strings.Split(targetModel, ":")
	if len(targetParts) == 1 {
		for _, m := range result.Models {
			nameParts := strings.Split(m.Name, ":")
			if len(nameParts) > 0 && nameParts[0] == targetParts[0] {
				return m.Size, nil
			}
		}
	}

	return 0, fmt.Errorf("model %s not found in local Ollama", targetModel)
}

// autoCalculateLogic isolates the pure calculation allowing dependency injection testing
func autoCalculateLogic(ramBytes uint64, modSizeBytes uint64, osType string) int {
	gbRAM := float64(ramBytes) / (1024 * 1024 * 1024)
	gbModel := float64(modSizeBytes) / (1024 * 1024 * 1024)

	var reserved float64
	switch osType {
	case "darwin":
		// macOS: max(8GB, 0.2 * total)
		reserved = math.Max(8.0, 0.2*gbRAM)
	case "linux", "windows":
		// Windows/Linux: max(6GB, 0.15 * total)
		reserved = math.Max(6.0, 0.15*gbRAM)
	default:
		reserved = math.Max(6.0, 0.15*gbRAM)
	}

	available := gbRAM - reserved

	// 【修复 1】重写 LLM 内存占用模型
	// 模型本体仅加载一次 (增加 10% 作为图计算/元数据冗余)
	baseModelMem := gbModel * 1.1

	if available <= baseModelMem {
		return 1 // 剩余内存勉强只够跑单线程
	}

	// 扣除模型本体后，剩下的内存全部用来分配给并发的 KV Cache
	availableForKV := available - baseModelMem

	// 预估每个并发请求消耗的 KV Cache (1.5GB 足够支撑大部分 4k-8k 上下文)
	kvCachePerRequest := 1.5

	recommended := int(math.Floor(availableForKV / kvCachePerRequest))

	// Bounding logic
	if recommended < 1 {
		recommended = 1
	}
	if recommended > 10 {
		recommended = 10
	}

	return recommended
}

func maxConcurrencyByCPU() int {
	cap := runtime.NumCPU() - 1
	if cap < 1 {
		return 1
	}
	if cap > 4 {
		return 4 // 强行限制最大并发为 4，防止桌面级 CPU 满载无响应
	}
	return cap
}

// 【修复 4】补充缺失的函数：根据模型量级限制并发，防止巨型模型挤爆显存/内存
func maxConcurrencyByModel(modelName string) int {
	name := strings.ToLower(modelName)
	if strings.Contains(name, "70b") || strings.Contains(name, "72b") {
		return 1
	}
	if strings.Contains(name, "32b") || strings.Contains(name, "34b") {
		return 2
	}
	return 5 // 14B 及以下的小模型，允许走到 CPU 限制的上限
}

// estimateModelSizeGBFromName guesses a model's memory footprint from its
// name (e.g. "mlx-community/Qwen2.5-32B-Instruct-4bit" -> ~20GB). Used when
// the backend exposes no size API (MLX / OpenAI-compatible servers), or as
// a fallback when Ollama's tags API is unreachable.
func estimateModelSizeGBFromName(modelName string) uint64 {
	name := strings.ToLower(modelName)
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*b`)
	match := re.FindStringSubmatch(name)
	if match == nil {
		return 4 // unknown size: assume a small model
	}
	params, err := strconv.ParseFloat(match[1], 64)
	if err != nil || params <= 0 {
		return 4
	}
	bytesPerParam := 2.0 // fp16 default
	quantBits := func(tokens ...string) bool {
		for _, tok := range tokens {
			if strings.Contains(name, tok) {
				return true
			}
		}
		return false
	}
	switch {
	case quantBits("4bit", "q4"):
		bytesPerParam = 0.6
	case quantBits("8bit", "q8"):
		bytesPerParam = 1.05
	case quantBits("6bit", "q6"):
		bytesPerParam = 0.8
	case quantBits("5bit", "q5"):
		bytesPerParam = 0.7
	case quantBits("3bit", "q3"):
		bytesPerParam = 0.45
	case strings.Contains(name, "mlx"):
		// Ollama "-mlx" tags and MLX-community conversions ship 4-bit as
		// the default quantization (e.g. "qwen3.8:27b-mlx" ≈ 16GB, not the
		// 54GB an fp16 guess would imply).
		bytesPerParam = 0.6
	}
	gb := params * bytesPerParam
	if gb < 1 {
		gb = 1
	}
	return uint64(gb * 1024 * 1024 * 1024)
}

// AutoCalculateConcurrency calculates a safe concurrency limit based on RAM
// and the model size. For Ollama it queries the tags API (falling back to a
// name-based estimate when unreachable); for MLX it estimates the model
// size from the name. Neither path ever touches or restarts any server.
func AutoCalculateConcurrency(apiURL, modelName, engine string) (*SystemInfo, error) {
	ram, err := getSystemRAMBytes()
	if err != nil {
		return &SystemInfo{RecommendedC: 1}, fmt.Errorf("failed to get system RAM: %v", err)
	}

	if engine != EngineOllama {
		modSize := estimateModelSizeGBFromName(modelName)
		recommended := autoCalculateLogic(ram, modSize, runtime.GOOS)
		cpuCap := maxConcurrencyByCPU()
		if recommended > cpuCap {
			recommended = cpuCap
		}
		modelCap := maxConcurrencyByModel(modelName)
		if recommended > modelCap {
			recommended = modelCap
		}
		return &SystemInfo{
			TotalRAMBytes: ram,
			ModelSize:     modSize,
			RecommendedC:  recommended,
			WarningMsg:    "",
		}, nil
	}

	modSize, err := getModelSizeBytes(apiURL, modelName)
	warning := ""
	if err != nil {
		// Tags API unreachable or model missing (e.g. Ollama restarting):
		// fall back to name-based estimation instead of degrading to
		// concurrency 1, so results stay consistent between runs.
		modSize = estimateModelSizeGBFromName(modelName)
		warning = fmt.Sprintf("⚠️ 无法从 Ollama 获取模型体积（%v），已按名称估算。", err)
	}

	recommended := autoCalculateLogic(ram, modSize, runtime.GOOS)
	cpuCap := maxConcurrencyByCPU()
	if recommended > cpuCap {
		recommended = cpuCap
	}
	modelCap := maxConcurrencyByModel(modelName)
	if recommended > modelCap {
		recommended = modelCap
	}

	info := &SystemInfo{
		TotalRAMBytes: ram,
		ModelSize:     modSize,
		RecommendedC:  recommended,
		WarningMsg:    warning,
	}

	return info, nil
}
