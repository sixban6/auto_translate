package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
	"auto_translate/pkg/webtask"
)

type TranslationTask struct {
	ID        string
	Status    string
	Total     int
	Current   int
	Config    *config.Config
	InputPath string
	OutPath   string
	MessageCh chan webtask.LogMsg
	Error     string
	Stats     processor.TranslationStats
	StartedAt time.Time
}

var (
	tasks = make(map[string]*TranslationTask)
	mu    sync.Mutex
)

func main() {
	// Ensure temp dir exists for uploads
	os.MkdirAll("temp_uploads", os.ModePerm)

	// Serve Static Files
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// Serve UI
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "web/templates/index.html")
	})

	// API Endpoint: Start Translation Upload
	http.HandleFunc("/api/translate", handleTranslateStart)

	// API Endpoint: SSE Progress Monitor
	http.HandleFunc("/api/progress", handleProgressSSE)

	// API Endpoint: Get Ollama Models
	http.HandleFunc("/api/models", handleModels)

	// API Endpoint: Download Final File
	http.HandleFunc("/api/download", handleDownload)
	// API Endpoint: Load Roles
	http.HandleFunc("/api/roles", handleRoles)
	// API Endpoint: Explain Config
	http.HandleFunc("/api/explain_config", handleExplainConfig)
	// API Endpoint: Download Failures
	http.HandleFunc("/api/download_failures", handleDownloadFailures)
	// API Endpoint: Get Task Status and Stats
	http.HandleFunc("/api/task_status", handleTaskStatus)

	port := getAvailablePort(4000)
	fmt.Printf("Web server is running beautifully at http://localhost:%d\n", port)

	// Open the browser automatically
	go openBrowser(fmt.Sprintf("http://localhost:%d", port))

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

// getAvailablePort returns an available port starting from the given startPort
func getAvailablePort(startPort int) int {
	for port := startPort; port < 65535; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port
		}
	}
	return startPort // Fallback to startPort if no ports are available
}

// openBrowser opens the specified URL in the default browser of the user.
func openBrowser(url string) {
	// Give the server a moment to start
	time.Sleep(500 * time.Millisecond)

	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		fmt.Printf("Failed to open browser automatically: %v\n", err)
	}
}

func handleTranslateStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	configFileStr := r.FormValue("config")
	var cfg config.Config
	if err := json.Unmarshal([]byte(configFileStr), &cfg); err != nil {
		http.Error(w, "Invalid config JSON", http.StatusBadRequest)
		return
	}
	cfg.AutoDetectAndCalculate()

	if cfg.PromptRole != "" {
		prompt, err := loadPromptByRole(cfg.PromptRole)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg.Prompt = prompt
	}
	if cfg.Prompt == "" {
		http.Error(w, "Missing prompt or prompt_role", http.StatusBadRequest)
		return
	}

	// Read file from multipart
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to read uploaded file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".txt" && ext != ".epub" {
		http.Error(w, "Unsupported file extension", http.StatusBadRequest)
		return
	}

	// Save temp input
	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())
	inputPath := filepath.Join("temp_uploads", taskID+ext)
	outPath := filepath.Join("temp_uploads", taskID+"_translated"+ext)

	out, err := os.Create(inputPath)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	io.Copy(out, file)
	out.Close()

	cfg.InputFile = inputPath
	cfg.OutputFile = outPath

	// Create Task Tracker
	task := &TranslationTask{
		ID:        taskID,
		Status:    "running",
		Config:    &cfg,
		InputPath: inputPath,
		OutPath:   outPath,
		MessageCh: make(chan webtask.LogMsg, 100),
	}

	mu.Lock()
	tasks[taskID] = task
	mu.Unlock()

	// Dispatch background translation routine
	go runTranslationTask(task)

	// Return initial response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"task_id": taskID})
}

func handleProgressSSE(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()

	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Keep connection open and send logs over SSE
	for {
		select {
		case msg, open := <-task.MessageCh:
			if !open {
				// Channel closed, task is finished
				return
			}
			msgData, _ := json.Marshal(msg)
			fmt.Fprintf(w, "data: %s\n\n", msgData)
			flusher.Flush()

			if msg.Status == "completed" || msg.Status == "error" {
				return
			}
		case <-r.Context().Done():
			// Client disconnected
			return
		}
	}
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()

	if !ok || task.Status != "completed" {
		http.Error(w, "File not ready or task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=translated_"+filepath.Base(task.InputPath))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, task.OutPath)
}

func handleDownloadFailures(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()

	if !ok || task.Status != "completed" {
		http.Error(w, "Task not found or not completed", http.StatusNotFound)
		return
	}

	if len(task.Stats.FailedBlocks) == 0 {
		http.Error(w, "No failures found for this task", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=failures_list.txt")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	var sb strings.Builder
	sb.WriteString("=====================================\n")
	sb.WriteString(fmt.Sprintf("翻译失败人工校对清单 (总计: %d条)\n", len(task.Stats.FailedBlocks)))
	sb.WriteString("=====================================\n\n")
	for i, fb := range task.Stats.FailedBlocks {
		sb.WriteString(fmt.Sprintf("【块ID: %s】 (错误: %s)\n", fb.ID, fb.Error))
		sb.WriteString("-------------------------------------\n")
		sb.WriteString(strings.TrimSpace(fb.OriginalText) + "\n\n")
		// if more than 1000 items, stop to avoid memory issues
		if i > 1000 {
			sb.WriteString(fmt.Sprintf("... (剩余 %d 条被省略) ...\n", len(task.Stats.FailedBlocks)-1000))
			break
		}
	}
	w.Write([]byte(sb.String()))
}

func handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()

	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": task.Status,
		"stats":  task.Stats,
	})
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	apiURL := r.URL.Query().Get("api_url")
	if apiURL == "" {
		apiURL = "http://localhost:11434/v1/chat/completions"
	}

	baseURL := strings.Split(apiURL, "/v1/")[0]
	if !strings.Contains(apiURL, "/v1/") || baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	tagsURL := baseURL + "/api/tags"

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(tagsURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Failed: %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, m.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"models": models})
}

func handleRoles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir("prompts")
	if err != nil {
		http.Error(w, "Failed to read prompts directory", http.StatusInternalServerError)
		return
	}

	type RoleInfo struct {
		Name    string `json:"name"`
		Preview string `json:"preview"`
	}

	roles := make([]RoleInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			roleName := strings.TrimSuffix(name, filepath.Ext(name))
			preview, _ := loadPromptByRole(roleName)
			roles = append(roles, RoleInfo{Name: roleName, Preview: preview})
		}
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"roles": roles})
}

func loadPromptByRole(role string) (string, error) {
	cleanRole := filepath.Base(role)
	if cleanRole == "." || cleanRole == string(filepath.Separator) || cleanRole == "" {
		return "", fmt.Errorf("invalid prompt_role")
	}
	filePath := filepath.Join("prompts", cleanRole+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("prompt role not found: %s", role)
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", fmt.Errorf("prompt role is empty: %s", role)
	}
	return prompt, nil
}

func handleExplainConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid config JSON", http.StatusBadRequest)
		return
	}
	// Automatically auto-detect with defaults based on memory and model
	cfg.AutoDetectAndCalculate()

	explanation := config.GetConfigExplanation(&cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"explanation": explanation,
		"concurrency": cfg.Concurrency,
		"chunk_size":  cfg.MaxChunkSize,
		"retries":     cfg.MaxRetries,
	})
}

// Background Task Runner
func runTranslationTask(t *TranslationTask) {
	defer close(t.MessageCh)
	startTime := time.Now()
	t.StartedAt = startTime
	etaEstimator := webtask.NewETAEstimator(0.25, 5)

	sendLog := func(msg, mType string) {
		elapsedSec := 0
		if !t.StartedAt.IsZero() {
			elapsedSec = int(time.Since(t.StartedAt).Seconds())
		}
		t.MessageCh <- webtask.LogMsg{
			Type:       mType,
			Message:    msg,
			Total:      t.Total,
			Current:    t.Current,
			Status:     t.Status,
			ElapsedSec: elapsedSec,
			EtaSec:     etaEstimator.Estimate(t.Current, t.Total, time.Since(t.StartedAt)),
		}
	}

	fail := func(err error) {
		t.Status = "error"
		t.Error = err.Error()
		t.MessageCh <- webtask.LogMsg{
			Type:       "red",
			Message:    fmt.Sprintf("❌ 发生严重错误: %v", err),
			Status:     "error",
			ElapsedSec: int(time.Since(startTime).Seconds()),
			EtaSec:     -1,
		}
	}
	ext := filepath.Ext(t.Config.InputFile)
	p, err := parser.GetParser(ext)
	if err != nil {
		fail(err)
		return
	}

	sendLog(fmt.Sprintf("开始解析文件 %s", ext), "gray")
	blocks, err := p.Extract(t.Config.InputFile)
	if err != nil {
		fail(err)
		return
	}

	t.Total = len(blocks)
	t.Current = 0
	sendLog(fmt.Sprintf("文件解析成功。总计抽取到 %d 个待翻译文本块段。", t.Total), "green")
	sendLog("启动翻译引擎...", "gray")

	if t.Config.SystemInfoMsg != "" {
		sendLog(t.Config.SystemInfoMsg, "gray")
	}
	if t.Config.SystemWarning != "" {
		if strings.Contains(t.Config.SystemWarning, "✅") {
			sendLog(t.Config.SystemWarning, "green")
		} else {
			sendLog("⚠️ "+t.Config.SystemWarning, "orange")
		}
	}

	tr := translator.New(t.Config)
	proc := processor.New(t.Config, tr)

	sendLog(fmt.Sprintf("引擎已并发启动 (Concurrency = %d). 请耐心等待...", t.Config.Concurrency), "gray")

	translatedBlocks, stats, err := proc.Process(blocks, func(current, total int, msg string) {
		t.Total = total
		if current >= 0 {
			t.Current = current
		}

		mType := "gray"
		if strings.Contains(msg, "❌") {
			mType = "red"
		} else if strings.Contains(msg, "⚠️") || strings.Contains(msg, "Retrying") {
			mType = "orange"
		} else if strings.Contains(msg, "✅") {
			mType = "green"
		}

		t.MessageCh <- webtask.LogMsg{
			Type:       mType,
			Message:    msg,
			Total:      total,
			Current:    t.Current,
			Status:     t.Status,
			ElapsedSec: int(time.Since(startTime).Seconds()),
			EtaSec:     etaEstimator.Estimate(t.Current, total, time.Since(startTime)),
		}
	})
	if err != nil {
		fail(err)
		return
	}

	t.Current = t.Total // Hack to show 100% since we executed in batch mode internally
	sendLog("所有块翻译完毕。汇编构建输出文件...", "gray")

	err = p.Assemble(translatedBlocks, t.Config.OutputFile, t.Config.Bilingual)
	if err != nil {
		fail(err)
		return
	}

	t.Stats = stats
	t.Status = "completed"
	sendLog(fmt.Sprintf("📊 翻译统计: 成功=%d 术语降级=%d 拒答=%d 完全失败=%d", stats.SuccessCount, stats.FallbackCount, stats.RefusedCount, stats.FailureCount), "gray")
	sendLog("🎉 生成最终电子书/文档成功！", "green")
	elapsed := time.Since(startTime)
	sendLog(fmt.Sprintf("⏱️ 翻译总耗时: %s", formatDuration(elapsed)), "green")

	t.MessageCh <- webtask.LogMsg{
		Status:     "completed",
		Total:      t.Total,
		Current:    t.Total,
		ElapsedSec: int(time.Since(startTime).Seconds()),
		EtaSec:     0,
	}
}

func formatDuration(d time.Duration) string {
	totalSeconds := int(d.Seconds())
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	mins := totalSeconds / 60
	secs := totalSeconds % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	hours := mins / 60
	mins = mins % 60
	return fmt.Sprintf("%dh%dm%ds", hours, mins, secs)
}
