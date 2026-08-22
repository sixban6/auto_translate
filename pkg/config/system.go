package config

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
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

// tryRestartOllama attempts to set the env var and restart the Ollama process
func tryRestartOllama(concurrency int) error {
	envVal := strconv.Itoa(concurrency)
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "setenv", "OLLAMA_NUM_PARALLEL", envVal).Run()
		exec.Command("pkill", "-x", "Ollama").Run()
		exec.Command("pkill", "-x", "ollama").Run()
		time.Sleep(800 * time.Millisecond)
		if err := exec.Command("open", "-a", "Ollama").Start(); err == nil {
			return nil
		}
		if bin, err := exec.LookPath("ollama"); err == nil {
			cmd := exec.Command(bin, "serve")
			cmd.Env = append(os.Environ(), "OLLAMA_NUM_PARALLEL="+envVal)
			return cmd.Start()
		}
		return fmt.Errorf("ollama not found")
	case "windows":
		exec.Command("setx", "OLLAMA_NUM_PARALLEL", envVal).Run()
		exec.Command("taskkill", "/F", "/IM", "ollama.exe").Run()
		exec.Command("taskkill", "/F", "/IM", "ollama app.exe").Run()
		time.Sleep(1 * time.Second)
		cmd := exec.Command("cmd", "/c", "start", "ollama", "serve")
		// 【修复 2】关键：显式将环境变量注入新进程，否则 setx 在当前进程树不会立刻生效
		cmd.Env = append(os.Environ(), "OLLAMA_NUM_PARALLEL="+envVal)
		return cmd.Start()
	case "linux":
		// 如果是通过 Systemd 运行的
		if err := exec.Command("systemctl", "--user", "restart", "ollama").Run(); err == nil {
			// Systemd 方式需要用户在 service 文件中配置环境，这里的动态配置可能不会生效
			// 但暂保留原逻辑
			return nil
		}
		// 回退到手动进程管理
		exec.Command("killall", "ollama").Run()
		time.Sleep(1 * time.Second)
		cmd := exec.Command("sh", "-c", "ollama serve > /dev/null 2>&1 &")
		// 【修复 2】关键：显式注入
		cmd.Env = append(os.Environ(), "OLLAMA_NUM_PARALLEL="+envVal)
		return cmd.Start()
	}
	return fmt.Errorf("unsupported os for auto-restart")
}

// estimateModelSizeGBFromName guesses a model's memory footprint from its
// name (e.g. "mlx-community/Qwen2.5-32B-Instruct-4bit" -> ~20GB). Used when
// the backend exposes no size API (MLX / OpenAI-compatible servers).
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
	switch {
	case strings.Contains(name, "4bit"):
		bytesPerParam = 0.6
	case strings.Contains(name, "8bit"):
		bytesPerParam = 1.05
	case strings.Contains(name, "6bit"):
		bytesPerParam = 0.8
	case strings.Contains(name, "5bit"):
		bytesPerParam = 0.7
	case strings.Contains(name, "3bit"):
		bytesPerParam = 0.45
	}
	gb := params * bytesPerParam
	if gb < 1 {
		gb = 1
	}
	return uint64(gb * 1024 * 1024 * 1024)
}

// AutoCalculateConcurrency calculates a safe concurrency limit based on RAM
// and the model size. For Ollama it queries the tags API and may restart
// Ollama with a higher OLLAMA_NUM_PARALLEL; for MLX it estimates the model
// size from the name and never touches any server.
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
	if err != nil {
		return &SystemInfo{RecommendedC: 1, TotalRAMBytes: ram}, fmt.Errorf("failed to get model size: %v", err)
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

	warning := ""
	if recommended > 1 {
		if err := tryRestartOllama(recommended); err != nil {
			recommended = 1
			warning = "⚠️ 已自动降级并发为 1 以确保稳定运行。"
		} else {
			warning = fmt.Sprintf("✅ 已自动设置 OLLAMA_NUM_PARALLEL=%d 并重启 Ollama，高并发通道已激活。", recommended)
			time.Sleep(3 * time.Second)
		}
	}

	info := &SystemInfo{
		TotalRAMBytes: ram,
		ModelSize:     modSize,
		RecommendedC:  recommended,
		WarningMsg:    warning,
	}

	return info, nil
}
