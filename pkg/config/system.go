package config

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
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
		// Get-CimInstance Win32_ComputerSystem | Select-Object -ExpandProperty TotalPhysicalMemory
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
	if strings.HasPrefix(apiURL, "http://") || strings.HasPrefix(apiURL, "https://") {
		parts := strings.Split(apiURL, "/")
		if len(parts) >= 3 {
			baseURL = parts[0] + "//" + parts[2]
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

	for _, m := range result.Models {
		nameParts := strings.Split(m.Name, ":")
		targetParts := strings.Split(targetModel, ":")

		if m.Name == targetModel || (len(nameParts) > 0 && len(targetParts) > 0 && nameParts[0] == targetParts[0]) {
			return m.Size, nil
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

	// Estimated Model Cost (1.6x safety budget)
	estimatedModelMem := gbModel * 1.6

	// Concurrency Math
	var recommended int = 1
	if estimatedModelMem > 0 && available >= estimatedModelMem { // guard zero size
		recommended = int(math.Floor(available / estimatedModelMem))
	} else if available <= 0 {
		recommended = 1 // Not enough memory above reserved
	}

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
	return cap
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
		return cmd.Start()
	case "linux":
		if err := exec.Command("systemctl", "--user", "restart", "ollama").Run(); err == nil {
			return nil
		}
		exec.Command("killall", "ollama").Run()
		time.Sleep(1 * time.Second)
		cmd := exec.Command("sh", "-c", fmt.Sprintf("OLLAMA_NUM_PARALLEL=%s ollama serve > /dev/null 2>&1 &", envVal))
		return cmd.Start()
	}
	return fmt.Errorf("unsupported os for auto-restart")
}

// AutoCalculateConcurrency calculates safe concurrency limit based on RAM and model size.
// Returns a filled SystemInfo or falls back to Concurrency=1 gracefully.
func AutoCalculateConcurrency(apiURL, modelName string) (*SystemInfo, error) {
	ram, err := getSystemRAMBytes()
	if err != nil {
		return &SystemInfo{RecommendedC: 1}, fmt.Errorf("failed to get system RAM: %v", err)
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
