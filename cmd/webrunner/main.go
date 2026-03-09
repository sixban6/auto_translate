package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"auto_translate/pkg/config"
	"auto_translate/pkg/keepalive"
	"auto_translate/pkg/parser"
	"auto_translate/pkg/processor"
	"auto_translate/pkg/translator"
	"auto_translate/pkg/webtask"
)

type TranslationTask struct {
	ID              string
	Status          string
	Total           int
	Current         int
	Config          *config.Config
	InputPath       string
	OutPath         string
	MessageCh       chan webtask.LogMsg
	Error           string
	Stats           processor.TranslationStats
	StartedAt       time.Time
	CompletedChunks map[string]string
	InstanceID      string
	LastHeartbeat   time.Time
	StatusReason    string
	OriginalFilename string
	Ctx             context.Context
	Cancel          context.CancelFunc
	StateMu         sync.Mutex
}

type TaskState struct {
	ID              string                     `json:"id"`
	Total           int                        `json:"total"`
	Current         int                        `json:"current"`
	Status          string                     `json:"status"`
	InputPath       string                     `json:"input_path"`
	OutPath         string                     `json:"out_path"`
	Config          *config.Config             `json:"config"`
	CompletedChunks map[string]string          `json:"completed_chunks"`
	Stats           processor.TranslationStats `json:"stats"`
	InstanceID      string                     `json:"instance_id"`
	LastHeartbeatTs int64                      `json:"last_heartbeat_ts"`
	StatusReason    string                     `json:"status_reason"`
	OriginalFilename string                    `json:"original_filename"`
}

func saveTaskState(t *TranslationTask) {
	t.StateMu.Lock()
	defer t.StateMu.Unlock()

	lastHeartbeat := t.LastHeartbeat
	if lastHeartbeat.IsZero() {
		lastHeartbeat = time.Now()
	}
	state := TaskState{
		ID:              t.ID,
		Total:           t.Total,
		Current:         t.Current,
		Status:          t.Status,
		InputPath:       t.InputPath,
		OutPath:         t.OutPath,
		Config:          t.Config,
		CompletedChunks: t.CompletedChunks,
		Stats:           t.Stats,
		InstanceID:      t.InstanceID,
		LastHeartbeatTs: lastHeartbeat.Unix(),
		StatusReason:    t.StatusReason,
		OriginalFilename: t.OriginalFilename,
	}

	statePath := t.InputPath + ".state.json"
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)
}

func safeSendTaskMessage(t *TranslationTask, msg webtask.LogMsg) {
	defer func() {
		recover()
	}()
	if t.MessageCh != nil {
		t.MessageCh <- msg
	}
}

var (
	tasks       = make(map[string]*TranslationTask)
	mu          sync.Mutex
	taskQueue   chan *TranslationTask
	instanceID  string
	maxParallel int
)

func main() {
	// Ensure temp dir exists for uploads
	os.MkdirAll("temp_uploads", os.ModePerm)
	instanceID = newInstanceID()
	maxParallel = getMaxParallel()
	if maxParallel < 1 {
		maxParallel = 1
	}
	taskQueue = make(chan *TranslationTask, 200)
	for i := 0; i < maxParallel; i++ {
		go taskWorker()
	}

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
	// API Endpoint: Resume Task
	http.HandleFunc("/api/resume", handleResume)
	// API Endpoint: List Tasks
	http.HandleFunc("/api/tasks", handleTasks)
	// API Endpoint: Pause Task
	http.HandleFunc("/api/pause", handlePause)

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
		ID:              taskID,
		Status:          "queued",
		Config:          &cfg,
		InputPath:       inputPath,
		OutPath:         outPath,
		MessageCh:       make(chan webtask.LogMsg, 100),
		CompletedChunks: make(map[string]string),
		InstanceID:      instanceID,
		OriginalFilename: header.Filename,
	}
	task.LastHeartbeat = time.Now()
	task.StatusReason = "queued"

	mu.Lock()
	tasks[taskID] = task
	mu.Unlock()

	saveTaskState(task)
	task.MessageCh <- webtask.LogMsg{
		Type:       "gray",
		Message:    "任务已进入队列，等待可用执行槽位...",
		Status:     "queued",
		Total:      0,
		Current:    0,
		ElapsedSec: 0,
		EtaSec:     -1,
	}
	taskQueue <- task

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

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

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
			return
		case <-ticker.C:
			fmt.Fprintf(w, "data: {\"type\": \"heartbeat\"}\n\n")
			flusher.Flush()
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
		// Try to recover from disk
		files, _ := filepath.Glob(filepath.Join("temp_uploads", taskID+"*.state.json"))
		if len(files) > 0 {
			var state TaskState
			data, err := os.ReadFile(files[0])
			if err == nil && json.Unmarshal(data, &state) == nil {
				status, resumeSupported, reason := resolveTaskState(state)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":           status,
					"stats":            state.Stats,
					"resume_supported": resumeSupported,
					"status_reason":    reason,
				})
				return
			}
		}
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	resumeSupported := task.Status == "error" || task.Status == "disconnected" || task.Status == "interrupted" || task.Status == "paused"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           task.Status,
		"stats":            task.Stats,
		"resume_supported": resumeSupported,
		"status_reason":    task.StatusReason,
	})
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	t, ok := tasks[taskID]
	mu.Unlock()

	var state TaskState
	if !ok {
		files, _ := filepath.Glob(filepath.Join("temp_uploads", taskID+"*.state.json"))
		if len(files) == 0 {
			http.Error(w, "State not found", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			http.Error(w, "Failed to read state", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(data, &state); err != nil {
			http.Error(w, "Failed to parse state", http.StatusInternalServerError)
			return
		}
		status, _, _ := resolveTaskState(state)
		state.Status = status

		t = &TranslationTask{
			ID:              state.ID,
			Status:          state.Status,
			Total:           state.Total,
			Current:         state.Current,
			Config:          state.Config,
			InputPath:       state.InputPath,
			OutPath:         state.OutPath,
			CompletedChunks: state.CompletedChunks,
			Stats:           state.Stats,
			InstanceID:      instanceID,
			StatusReason:    state.StatusReason,
		}
		t.LastHeartbeat = time.Unix(state.LastHeartbeatTs, 0)

		mu.Lock()
		tasks[taskID] = t
		mu.Unlock()
	}

	if t.Status == "running" {
		http.Error(w, "Task is already running", http.StatusConflict)
		return
	}

	t.Status = "queued"
	t.StatusReason = "resume_queued"
	t.Error = ""
	t.MessageCh = make(chan webtask.LogMsg, 100)
	t.InstanceID = instanceID
	t.LastHeartbeat = time.Now()
	saveTaskState(t)

	taskQueue <- t

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"task_id": t.ID, "status": "resumed"})
}

func handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	t, ok := tasks[taskID]
	mu.Unlock()
	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	if t.Status != "running" && t.Status != "queued" {
		http.Error(w, "Task not running or queued", http.StatusConflict)
		return
	}
	if t.Cancel != nil {
		t.Cancel()
	}
	t.Status = "paused"
	t.StatusReason = "paused_by_user"
	saveTaskState(t)
	safeSendTaskMessage(t, webtask.LogMsg{
		Type:    "orange",
		Message: "任务已暂停",
		Status:  "paused",
		Total:   t.Total,
		Current: t.Current,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"task_id": t.ID, "status": "paused"})
}

func handleTasks(w http.ResponseWriter, r *http.Request) {
	files, _ := filepath.Glob(filepath.Join("temp_uploads", "*.state.json"))
	type taskSummary struct {
		ID              string                     `json:"id"`
		Status          string                     `json:"status"`
		Total           int                        `json:"total"`
		Current         int                        `json:"current"`
		InputPath       string                     `json:"input_path"`
		OutPath         string                     `json:"out_path"`
		UpdatedAt       int64                      `json:"updated_at"`
		ResumeSupported bool                       `json:"resume_supported"`
		StatusReason    string                     `json:"status_reason"`
		Stats           processor.TranslationStats `json:"stats"`
		OriginalFilename string                    `json:"original_filename"`
	}
	type fileInfo struct {
		path string
		mod  time.Time
	}
	var infos []fileInfo
	for _, f := range files {
		stat, err := os.Stat(f)
		if err != nil {
			continue
		}
		infos = append(infos, fileInfo{path: f, mod: stat.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].mod.After(infos[j].mod) })
	if len(infos) > 50 {
		infos = infos[:50]
	}
	summaries := make([]taskSummary, 0, len(infos))
	for _, info := range infos {
		var state TaskState
		data, err := os.ReadFile(info.path)
		if err != nil {
			continue
		}
		if json.Unmarshal(data, &state) != nil {
			continue
		}
		status, resumeSupported, reason := resolveTaskState(state)
		summaries = append(summaries, taskSummary{
			ID:              state.ID,
			Status:          status,
			Total:           state.Total,
			Current:         state.Current,
			InputPath:       state.InputPath,
			OutPath:         state.OutPath,
			UpdatedAt:       info.mod.Unix(),
			ResumeSupported: resumeSupported,
			StatusReason:    reason,
			Stats:           state.Stats,
			OriginalFilename: state.OriginalFilename,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": summaries,
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
	defer keepalive.PreventSleep()()
	defer close(t.MessageCh)
	startTime := time.Now()
	t.StartedAt = startTime
	t.Status = "running"
	t.StatusReason = ""
	t.InstanceID = instanceID
	t.LastHeartbeat = time.Now()
	if t.Cancel != nil {
		t.Cancel()
	}
	t.Ctx, t.Cancel = context.WithCancel(context.Background())
	etaEstimator := webtask.NewETAEstimator(0.25, 5)
	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		flush := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		defer flush.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				t.LastHeartbeat = time.Now()
			case <-flush.C:
				saveTaskState(t)
			}
		}
	}()
	defer close(heartbeatStop)

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
		t.StatusReason = "fatal_error"
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
	saveTaskState(t)

	translatedBlocks, stats, err := proc.Process(t.Ctx, blocks, t.CompletedChunks, func(current, total int, msg string) {
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
	}, func(chunkID, translatedText string) {
		t.StateMu.Lock()
		if t.CompletedChunks == nil {
			t.CompletedChunks = make(map[string]string)
		}
		t.CompletedChunks[chunkID] = translatedText
		t.StateMu.Unlock()
		saveTaskState(t)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			t.Status = "paused"
			t.StatusReason = "paused_by_user"
			saveTaskState(t)
			safeSendTaskMessage(t, webtask.LogMsg{
				Type:       "orange",
				Message:    "任务已暂停",
				Status:     "paused",
				ElapsedSec: int(time.Since(startTime).Seconds()),
				EtaSec:     -1,
			})
			return
		}
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
	t.StatusReason = ""
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

func resolveTaskState(state TaskState) (string, bool, string) {
	status := state.Status
	reason := state.StatusReason
	now := time.Now()
	if status == "running" || status == "queued" {
		if state.InstanceID != "" && state.InstanceID != instanceID {
			status = "interrupted"
			reason = "instance_mismatch"
		} else {
			if state.LastHeartbeatTs == 0 {
				status = "interrupted"
				reason = "heartbeat_missing"
			} else if now.Sub(time.Unix(state.LastHeartbeatTs, 0)) > 3*time.Minute {
				status = "interrupted"
				reason = "heartbeat_timeout"
			}
		}
	}

	// Fix: If task is interrupted but actually finished (100% and output file exists), mark as completed
	if status == "interrupted" {
		if state.Total > 0 && state.Current >= state.Total {
			if _, err := os.Stat(state.OutPath); err == nil {
				status = "completed"
				reason = "recovered_completion"
			}
		}
	}

	if status == "error" && reason == "" {
		reason = "error"
	}
	resumeSupported := status == "error" || status == "disconnected" || status == "interrupted" || status == "paused"
	return status, resumeSupported, reason
}

func taskWorker() {
	for task := range taskQueue {
		if task.Status == "paused" {
			continue
		}
		runTranslationTask(task)
	}
}

func newInstanceID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("instance-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func getMaxParallel() int {
	val := strings.TrimSpace(os.Getenv("TRANSLATE_MAX_PARALLEL"))
	if val == "" {
		return 1
	}
	num, err := strconv.Atoi(val)
	if err != nil {
		return 1
	}
	return num
}
