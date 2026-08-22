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
	"math"
	"net"
	"net/http"
	"net/url"
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
	SrcFileName     string
	Ctx             context.Context
	Cancel          context.CancelFunc
	// StopExportRequested marks a task the user asked to terminate early and
	// export with whatever has been translated so far.
	StopExportRequested   bool
	StateMu               sync.Mutex
	LastResumeAt          time.Time
	ElapsedSecAccumulated int64
	DoneCh                chan struct{}
}

type TaskState struct {
	ID                    string                     `json:"id"`
	Total                 int                        `json:"total"`
	Current               int                        `json:"current"`
	Status                string                     `json:"status"`
	InputPath             string                     `json:"input_path"`
	OutPath               string                     `json:"out_path"`
	Config                *config.Config             `json:"config"`
	CompletedChunks       map[string]string          `json:"completed_chunks"`
	Stats                 processor.TranslationStats `json:"stats"`
	InstanceID            string                     `json:"instance_id"`
	LastHeartbeatTs       int64                      `json:"last_heartbeat_ts"`
	StatusReason          string                     `json:"status_reason"`
	SrcFileName           string                     `json:"src_file_name"`
	StartedAt             int64                      `json:"started_at"`
	LastResumeAt          int64                      `json:"last_resume_at"`
	ElapsedSecAccumulated int64                      `json:"elapsed_sec_accumulated"`
	OriginalFilename      string                     `json:"original_filename,omitempty"`
}

const (
	runtimeStatesDir = "temp_uploads/runtime_states"
	historyStatesDir = "temp_uploads/history_states"
)

var stateWriteMu sync.Mutex

func saveTaskState(t *TranslationTask) {
	if t.Status == "deleted" {
		return
	}
	t.StateMu.Lock()
	defer t.StateMu.Unlock()

	lastHeartbeat := t.LastHeartbeat
	if lastHeartbeat.IsZero() {
		lastHeartbeat = time.Now()
	}
	state := TaskState{
		ID:                    t.ID,
		Total:                 t.Total,
		Current:               t.Current,
		Status:                t.Status,
		InputPath:             t.InputPath,
		OutPath:               t.OutPath,
		Config:                t.Config,
		CompletedChunks:       t.CompletedChunks,
		Stats:                 t.Stats,
		InstanceID:            t.InstanceID,
		LastHeartbeatTs:       lastHeartbeat.Unix(),
		StatusReason:          t.StatusReason,
		SrcFileName:           t.SrcFileName,
		StartedAt:             unixOrZero(t.StartedAt),
		LastResumeAt:          unixOrZero(t.LastResumeAt),
		ElapsedSecAccumulated: t.ElapsedSecAccumulated,
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	stateWriteMu.Lock()
	ensureStateDirs()
	statePath := statePathForStatus(t.InputPath, t.Status)
	os.WriteFile(statePath, data, 0644)
	if statePath == runtimeStatePath(t.InputPath) {
		removeIfExists(historyStatePath(t.InputPath))
	} else {
		removeIfExists(runtimeStatePath(t.InputPath))
	}
	stateWriteMu.Unlock()
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func normalizeState(state *TaskState) {
	if state.SrcFileName == "" && state.OriginalFilename != "" {
		state.SrcFileName = state.OriginalFilename
	}
}

func computeEtaSec(current, total int, elapsedSec int64) int {
	if total <= 0 || current <= 0 {
		return -1
	}
	if current >= total {
		return 0
	}
	if elapsedSec <= 0 {
		return -1
	}
	speed := float64(elapsedSec) / float64(current)
	if speed <= 0 {
		return -1
	}
	remaining := total - current
	if remaining < 0 {
		remaining = 0
	}
	eta := int(math.Ceil(speed * float64(remaining)))
	if eta < 0 {
		return 0
	}
	if eta > 86400 {
		return 86400
	}
	return eta
}

func currentElapsedSec(t *TranslationTask, now time.Time) int64 {
	elapsed := t.ElapsedSecAccumulated
	if !t.LastResumeAt.IsZero() {
		delta := now.Sub(t.LastResumeAt)
		if delta > 0 {
			elapsed += int64(delta.Seconds())
		}
	}
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func stateElapsedSec(state TaskState, now time.Time, status string) int64 {
	elapsed := state.ElapsedSecAccumulated
	if status == "running" && state.LastResumeAt > 0 {
		delta := now.Unix() - state.LastResumeAt
		if delta > 0 {
			elapsed += delta
		}
	}
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func accumulateElapsed(t *TranslationTask, now time.Time) {
	if t.LastResumeAt.IsZero() {
		return
	}
	delta := now.Sub(t.LastResumeAt)
	if delta > 0 {
		t.ElapsedSecAccumulated += int64(delta.Seconds())
	}
	t.LastResumeAt = time.Time{}
	if t.ElapsedSecAccumulated < 0 {
		t.ElapsedSecAccumulated = 0
	}
}

func ensureStateDirs() {
	os.MkdirAll(runtimeStatesDir, os.ModePerm)
	os.MkdirAll(historyStatesDir, os.ModePerm)
}

func stateFileName(inputPath string) string {
	return filepath.Base(inputPath) + ".state.json"
}

func runtimeStatePath(inputPath string) string {
	return filepath.Join(runtimeStatesDir, stateFileName(inputPath))
}

func historyStatePath(inputPath string) string {
	return filepath.Join(historyStatesDir, stateFileName(inputPath))
}

func statePathForStatus(inputPath, status string) string {
	if status == "running" || status == "queued" {
		return runtimeStatePath(inputPath)
	}
	return historyStatePath(inputPath)
}

func removeIfExists(path string) {
	if path == "" {
		return
	}
	os.Remove(path)
}

func latestStateFile(dir, taskID string) string {
	files, _ := filepath.Glob(filepath.Join(dir, taskID+"*.state.json"))
	if len(files) == 0 {
		return ""
	}
	sort.Slice(files, func(i, j int) bool {
		infoI, errI := os.Stat(files[i])
		infoJ, errJ := os.Stat(files[j])
		if errI != nil || errJ != nil {
			return files[i] < files[j]
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})
	return files[0]
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
	ensureStateDirs()
	instanceID = newInstanceID()
	maxParallel = getMaxParallel()
	if maxParallel < 1 {
		maxParallel = 1
	}
	taskQueue = make(chan *TranslationTask, 200)
	for i := 0; i < maxParallel; i++ {
		go taskWorker()
	}

	// Serve Static Files. no-cache forces revalidation so UI updates are
	// picked up immediately instead of being served from browser heuristics.
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("web/static")))
	http.Handle("/static/", noCache(staticHandler))

	// Serve UI
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
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
	http.HandleFunc("/api/tasks/", handleTaskByID)
	// API Endpoint: Pause Task
	http.HandleFunc("/api/pause", handlePause)
	http.HandleFunc("/api/stop_export", handleStopExport)

	port := getAvailablePort(4000)
	fmt.Printf("Web server is running beautifully at http://localhost:%d\n", port)

	// Open the browser automatically
	go openBrowser(fmt.Sprintf("http://localhost:%d", port))

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
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
		SrcFileName:     header.Filename,
		DoneCh:          make(chan struct{}),
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

	var inputPath, outPath string
	var cfg *config.Config

	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()
	if ok {
		if task.Status != "completed" {
			http.Error(w, "File not ready or task not found", http.StatusNotFound)
			return
		}
		inputPath = task.InputPath
		outPath = task.OutPath
		cfg = task.Config
	} else {
		statePath := latestStateFile(runtimeStatesDir, taskID)
		histPath := latestStateFile(historyStatesDir, taskID)
		if stateFileModTime(histPath).After(stateFileModTime(statePath)) {
			statePath = histPath
		}
		if statePath == "" {
			http.Error(w, "File not ready or task not found", http.StatusNotFound)
			return
		}
		var state TaskState
		data, err := os.ReadFile(statePath)
		if err != nil || json.Unmarshal(data, &state) != nil {
			http.Error(w, "Failed to read task state", http.StatusInternalServerError)
			return
		}
		normalizeState(&state)
		status, _, _ := resolveTaskState(state)
		if status != "completed" {
			http.Error(w, "File not ready or task not found", http.StatusNotFound)
			return
		}
		inputPath = state.InputPath
		outPath = state.OutPath
		cfg = state.Config
	}

	if err := ensureOutputFresh(inputPath, outPath, cfg); err != nil {
		http.Error(w, "Failed to prepare translated file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=translated_"+filepath.Base(inputPath))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, outPath)
}

func stateFileModTime(path string) time.Time {
	if st, err := os.Stat(path); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// ensureOutputFresh rebuilds the output file when a state file has been
// modified more recently than the output file (e.g. hand-edited fixes to
// completed_chunks), or when the output file is missing. On rebuild, chunks
// are read from the newest state file so hand edits always win over stale
// in-memory data.
func ensureOutputFresh(inputPath, outPath string, cfg *config.Config) error {
	outStat, outErr := os.Stat(outPath)
	if outErr != nil {
		if !os.IsNotExist(outErr) {
			return outErr
		}
	} else {
		runtimePath := runtimeStatePath(inputPath)
		historyPath := historyStatePath(inputPath)
		newestState := stateFileModTime(runtimePath)
		if stateFileModTime(historyPath).After(newestState) {
			newestState = stateFileModTime(historyPath)
		}
		if !newestState.After(outStat.ModTime()) {
			return nil // output is already up to date
		}
	}

	statePath := runtimeStatePath(inputPath)
	if stateFileModTime(historyStatePath(inputPath)).After(stateFileModTime(statePath)) {
		statePath = historyStatePath(inputPath)
	}
	var completedChunks map[string]string
	if data, err := os.ReadFile(statePath); err == nil {
		var state TaskState
		if json.Unmarshal(data, &state) == nil {
			completedChunks = state.CompletedChunks
		}
	}

	p, err := parser.GetParser(strings.ToLower(filepath.Ext(inputPath)))
	if err != nil {
		return err
	}
	blocks, err := p.Extract(inputPath)
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	proc := processor.New(cfg, nil)
	translatedBlocks := proc.Reassemble(blocks, completedChunks)
	if err := p.Assemble(translatedBlocks, outPath, cfg.Bilingual); err != nil {
		return err
	}
	return nil
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
		statePath := latestStateFile(runtimeStatesDir, taskID)
		if statePath == "" {
			statePath = latestStateFile(historyStatesDir, taskID)
		}
		if statePath != "" {
			var state TaskState
			data, err := os.ReadFile(statePath)
			if err == nil && json.Unmarshal(data, &state) == nil {
				normalizeState(&state)
				status, resumeSupported, reason := resolveTaskState(state)
				elapsedSec := stateElapsedSec(state, time.Now(), status)
				etaSec := computeEtaSec(state.Current, state.Total, elapsedSec)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":           status,
					"stats":            state.Stats,
					"resume_supported": resumeSupported,
					"status_reason":    reason,
					"elapsed_sec":      elapsedSec,
					"eta_sec":          etaSec,
					"src_file_name":    state.SrcFileName,
				})
				return
			}
		}
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	resumeSupported := task.Status == "error" || task.Status == "disconnected" || task.Status == "interrupted" || task.Status == "paused"

	w.Header().Set("Content-Type", "application/json")
	elapsedSec := currentElapsedSec(task, time.Now())
	etaSec := computeEtaSec(task.Current, task.Total, elapsedSec)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           task.Status,
		"stats":            task.Stats,
		"resume_supported": resumeSupported,
		"status_reason":    task.StatusReason,
		"elapsed_sec":      elapsedSec,
		"eta_sec":          etaSec,
		"src_file_name":    task.SrcFileName,
	})
}

// applyResumeConfig overlays user-editable settings from a resume request
// onto a task's stored config, so a paused task can continue under a
// different model/engine. Zero or empty fields in the request keep the
// original values; the file bindings are never touched. It reports whether
// the model, engine or API endpoint actually changed.
func applyResumeConfig(dst *config.Config, src *config.Config, present map[string]bool) bool {
	if dst == nil || src == nil {
		return false
	}
	switched := false
	if src.Engine != "" && src.Engine != dst.Engine {
		dst.Engine = src.Engine
		switched = true
	}
	if src.Model != "" && src.Model != dst.Model {
		dst.Model = src.Model
		switched = true
	}
	if src.APIURL != "" && src.APIURL != dst.APIURL {
		dst.APIURL = src.APIURL
		switched = true
	}
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.Temperature > 0 {
		dst.Temperature = src.Temperature
	}
	if src.MaxChunkSize > 0 {
		dst.MaxChunkSize = src.MaxChunkSize
	}
	if src.MaxRetries > 0 {
		dst.MaxRetries = src.MaxRetries
	}
	if src.RequestTimeoutSec > 0 {
		dst.RequestTimeoutSec = src.RequestTimeoutSec
	}
	if len(src.Glossary) > 0 {
		dst.Glossary = src.Glossary
	}
	if src.PromptRole != "" {
		dst.PromptRole = src.PromptRole
	}
	if src.Prompt != "" {
		dst.Prompt = src.Prompt
	}
	if present["bilingual"] {
		dst.Bilingual = src.Bilingual
	}
	if switched {
		// Re-plan auto-tuned runtime settings for the new engine/model when
		// the request did not pin them explicitly. The batch size is restored
		// afterwards so completed-chunk keys stay aligned with the original
		// run (legacy per-block keys cover any mismatch anyway).
		keepChunk := dst.MaxChunkSize
		if src.Concurrency > 0 {
			dst.Concurrency = src.Concurrency
		} else {
			dst.Concurrency = 0
		}
		dst.AutoDetectAndCalculate()
		if src.MaxChunkSize <= 0 {
			dst.MaxChunkSize = keepChunk
		}
	}
	return switched
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")

	// Optional JSON body: a fresh config from the UI so the user can switch
	// model, engine or parameters before resuming. Omitted fields keep the
	// task's original values, so a bare POST behaves exactly like before.
	var override config.Config
	var present map[string]bool
	if r.Body != nil {
		reqBody, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
		if len(reqBody) > 0 {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(reqBody, &raw); err != nil {
				http.Error(w, "Invalid config JSON: "+err.Error(), http.StatusBadRequest)
				return
		}
			if err := json.Unmarshal(reqBody, &override); err != nil {
				http.Error(w, "Invalid config JSON: "+err.Error(), http.StatusBadRequest)
				return
		}
			present = make(map[string]bool, len(raw))
			for k := range raw {
				present[k] = true
		}
			if override.PromptRole != "" {
				prompt, err := loadPromptByRole(override.PromptRole)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				override.Prompt = prompt
			}
		}
	}

	mu.Lock()
	t, ok := tasks[taskID]
	mu.Unlock()

	var state TaskState
	if !ok {
		statePath := latestStateFile(runtimeStatesDir, taskID)
		if statePath == "" {
			statePath = latestStateFile(historyStatesDir, taskID)
		}
		if statePath == "" {
			http.Error(w, "State not found", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(statePath)
		if err != nil {
			http.Error(w, "Failed to read state", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(data, &state); err != nil {
			http.Error(w, "Failed to parse state", http.StatusInternalServerError)
			return
		}
		normalizeState(&state)
		if state.Config == nil {
			// Hand-written/minimal state files may omit the config; rebuild a
			// sane default so the resumed task does not crash.
			state.Config = &config.Config{
				InputFile:  state.InputPath,
				OutputFile: state.OutPath,
			}
			state.Config.AutoDetectAndCalculate()
		}
		status, _, _ := resolveTaskState(state)
		state.Status = status

		t = &TranslationTask{
			ID:                    state.ID,
			Status:                state.Status,
			Total:                 state.Total,
			Current:               state.Current,
			Config:                state.Config,
			InputPath:             state.InputPath,
			OutPath:               state.OutPath,
			CompletedChunks:       state.CompletedChunks,
			Stats:                 state.Stats,
			InstanceID:            instanceID,
			StatusReason:          state.StatusReason,
			SrcFileName:           state.SrcFileName,
			ElapsedSecAccumulated: state.ElapsedSecAccumulated,
			DoneCh:                make(chan struct{}),
		}
		if state.StartedAt > 0 {
			t.StartedAt = time.Unix(state.StartedAt, 0)
		}
		if state.LastResumeAt > 0 {
			t.LastResumeAt = time.Unix(state.LastResumeAt, 0)
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

	if present != nil && t.Config != nil {
		if applyResumeConfig(t.Config, &override, present) {
			safeSendTaskMessage(t, webtask.LogMsg{
				Type:    "gray",
				Message: fmt.Sprintf("🔁 恢复时已应用新配置：模型 %s（引擎 %s）", t.Config.Model, config.EngineLabel(t.Config.Engine)),
			})
		}
	}
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

// buildPartialOutput reassembles the output file purely from the chunks that
// have been translated so far (no model calls); paragraphs without a
// translation keep their original text. Returns how many blocks carry a real
// translation and the total block count.
func buildPartialOutput(t *TranslationTask, p parser.Parser, blocks []parser.TextBlock) (int, int, error) {
	t.StateMu.Lock()
	completed := make(map[string]string, len(t.CompletedChunks))
	for k, v := range t.CompletedChunks {
		completed[k] = v
	}
	t.StateMu.Unlock()

	proc := processor.New(t.Config, translator.New(t.Config))
	translatedBlocks := proc.Reassemble(blocks, completed)

	translatedCount := 0
	for i, b := range blocks {
		got := strings.TrimSpace(translatedBlocks[i].TranslatedText)
		if got != "" && got != strings.TrimSpace(b.OriginalText) {
			translatedCount++
		}
	}
	return translatedCount, len(blocks), p.Assemble(translatedBlocks, t.Config.OutputFile, t.Config.Bilingual)
}

// finishPartialExport marks a terminated-early task as completed and notifies
// listeners.
func finishPartialExport(t *TranslationTask, translatedCount, total int) {
	if t.Status == "deleted" {
		return
	}
	accumulateElapsed(t, time.Now())
	t.Status = "completed"
	t.StatusReason = "stopped_partial"
	t.Current = t.Total
	saveTaskState(t)
	// Same contract as normal completion: bump the output mtime after the
	// state save so download's freshness check does not rebuild the file.
	if now := time.Now(); t.OutPath != "" {
		os.Chtimes(t.OutPath, now, now)
	}
	safeSendTaskMessage(t, webtask.LogMsg{
		Type:    "orange",
		Message: fmt.Sprintf("🛑 已按要求终止翻译，未完成部分保留原文（已翻译 %d/%d 段）", translatedCount, total),
	})
	safeSendTaskMessage(t, webtask.LogMsg{
		Type:    "green",
		Message: "🎉 部分译本已生成，可下载查看",
		Status:  "completed",
		Total:   t.Total,
		Current: t.Total,
	})
}

// handleStopExport terminates an active task early and exports the output
// file built from the paragraphs translated so far. For running tasks the
// worker assembles the file after cancellation; paused/queued tasks are
// assembled inline.
func handleStopExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.URL.Query().Get("task_id")
	mu.Lock()
	t, ok := tasks[taskID]
	if !ok {
		mu.Unlock()
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	status := t.Status
	if t.StopExportRequested {
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"task_id": taskID, "status": "stopping"})
		return
	}
	if status != "running" && status != "queued" && status != "paused" {
		mu.Unlock()
		http.Error(w, "Task not active", http.StatusConflict)
		return
	}
	t.StopExportRequested = true
	if status != "running" {
		// Keep the worker from picking the task up mid-export; its pickup
		// check skips paused tasks.
		t.Status = "paused"
		t.StatusReason = "stopped_by_user"
	}
	cancel := t.Cancel
	mu.Unlock()

	if status == "running" {
		if cancel != nil {
			cancel()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"task_id": taskID, "status": "stopping"})
		return
	}

	// Paused/queued: export inline from the persisted chunks.
	p, err := parser.GetParser(strings.ToLower(filepath.Ext(t.InputPath)))
	if err == nil {
		var blocks []parser.TextBlock
		blocks, err = p.Extract(t.InputPath)
		if err == nil {
			var translatedCount, total int
			translatedCount, total, err = buildPartialOutput(t, p, blocks)
			if err == nil {
				finishPartialExport(t, translatedCount, total)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"task_id": taskID, "status": "completed",
					"translated": translatedCount, "total": total,
				})
				return
			}
		}
	}
	// Export failed: put the task back so it stays resumable.
	mu.Lock()
	t.Status = "paused"
	t.StatusReason = "stop_export_failed"
	t.StopExportRequested = false
	saveTaskState(t)
	mu.Unlock()
	http.Error(w, fmt.Sprintf("export failed: %v", err), http.StatusInternalServerError)
}

func handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleTasksList(w, r)
	case http.MethodDelete:
		handleTasksBatchDelete(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// handleTasksBatchDelete deletes many tasks at once. The ids come from the
// optional JSON body {"ids": [...]}; with no ids, every task known to this
// instance (history state files, runtime state files and in-memory tasks)
// is removed.
func handleTasksBatchDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if r.Body != nil {
		reqBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil && len(reqBody) > 0 {
			if err := json.Unmarshal(reqBody, &req); err != nil {
				http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
	}

	ids := req.IDs
	if len(ids) == 0 {
		ids = allTaskIDs()
	}

	// Phase 1: cancel every in-memory running task first so their workers
	// wind down in parallel; otherwise each delete would block serially for
	// up to 10 seconds on its own worker.
	var doneChs []chan struct{}
	for _, id := range ids {
		mu.Lock()
		t, ok := tasks[id]
		if ok && t.Status == "running" {
			t.Status = "deleted"
			t.StatusReason = "deleted"
			if t.Cancel != nil {
				t.Cancel()
			}
			if t.DoneCh != nil {
				doneChs = append(doneChs, t.DoneCh)
			}
		}
		mu.Unlock()
	}
	for _, ch := range doneChs {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
		}
	}

	deleted := 0
	var failedIDs []string
	for _, id := range ids {
		if id == "" || strings.Contains(id, "/") {
			failedIDs = append(failedIDs, id)
			continue
		}
		deleteTaskByID(id)
		deleted++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": deleted,
		"failed":  failedIDs,
	})
}

// allTaskIDs enumerates every task id known to this instance: state files
// on disk (history + runtime) plus in-memory tasks.
func allTaskIDs() []string {
	seen := make(map[string]struct{})
	for _, dir := range []string{historyStatesDir, runtimeStatesDir} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.state.json"))
		for _, f := range files {
			// State files are named "<taskID><ext>.state.json".
			name := strings.TrimSuffix(filepath.Base(f), ".state.json")
			name = strings.TrimSuffix(name, ".epub")
			name = strings.TrimSuffix(name, ".txt")
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	mu.Lock()
	for id := range tasks {
		seen[id] = struct{}{}
	}
	mu.Unlock()
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

func handleTasksList(w http.ResponseWriter, r *http.Request) {
	files, _ := filepath.Glob(filepath.Join(historyStatesDir, "*.state.json"))
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
		SrcFileName     string                     `json:"src_file_name"`
		ElapsedSec      int64                      `json:"elapsed_sec"`
		EtaSec          int                        `json:"eta_sec"`
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
		normalizeState(&state)
		status, resumeSupported, reason := resolveTaskState(state)
		elapsedSec := stateElapsedSec(state, time.Now(), status)
		etaSec := computeEtaSec(state.Current, state.Total, elapsedSec)
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
			SrcFileName:     state.SrcFileName,
			ElapsedSec:      elapsedSec,
			EtaSec:          etaSec,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": summaries,
	})
}

func handleTaskByID(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if taskID == "" || strings.Contains(taskID, "/") {
		http.Error(w, "Invalid task id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case "GET":
		handleTaskGetByID(w, r, taskID)
	case "DELETE":
		handleTaskDeleteByID(w, r, taskID)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleTaskGetByID(w http.ResponseWriter, r *http.Request, taskID string) {
	mu.Lock()
	task, ok := tasks[taskID]
	mu.Unlock()
	if ok {
		elapsedSec := currentElapsedSec(task, time.Now())
		etaSec := computeEtaSec(task.Current, task.Total, elapsedSec)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":               task.ID,
			"status":           task.Status,
			"total":            task.Total,
			"current":          task.Current,
			"input_path":       task.InputPath,
			"out_path":         task.OutPath,
			"resume_supported": task.Status == "error" || task.Status == "disconnected" || task.Status == "interrupted" || task.Status == "paused",
			"status_reason":    task.StatusReason,
			"stats":            task.Stats,
			"src_file_name":    task.SrcFileName,
			"elapsed_sec":      elapsedSec,
			"eta_sec":          etaSec,
		})
		return
	}
	statePath := latestStateFile(runtimeStatesDir, taskID)
	if statePath == "" {
		statePath = latestStateFile(historyStatesDir, taskID)
	}
	if statePath == "" {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	var state TaskState
	data, err := os.ReadFile(statePath)
	if err != nil || json.Unmarshal(data, &state) != nil {
		http.Error(w, "Failed to read state", http.StatusInternalServerError)
		return
	}
	normalizeState(&state)
	status, resumeSupported, reason := resolveTaskState(state)
	elapsedSec := stateElapsedSec(state, time.Now(), status)
	etaSec := computeEtaSec(state.Current, state.Total, elapsedSec)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":               state.ID,
		"status":           status,
		"total":            state.Total,
		"current":          state.Current,
		"input_path":       state.InputPath,
		"out_path":         state.OutPath,
		"resume_supported": resumeSupported,
		"status_reason":    reason,
		"stats":            state.Stats,
		"src_file_name":    state.SrcFileName,
		"elapsed_sec":      elapsedSec,
		"eta_sec":          etaSec,
	})
}

func handleTaskDeleteByID(w http.ResponseWriter, r *http.Request, taskID string) {
	deleteTaskByID(taskID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"task_id": taskID, "status": "deleted"})
}

// deleteTaskByID removes a task from memory, cancels it when running, and
// deletes every associated file (upload, output, state files).
func deleteTaskByID(taskID string) {
	var inputPath string
	var outPath string
	var doneCh chan struct{}
	var status string

	mu.Lock()
	task, ok := tasks[taskID]
	if ok {
		status = task.Status
		inputPath = task.InputPath
		outPath = task.OutPath
		doneCh = task.DoneCh
		task.Status = "deleted"
		task.StatusReason = "deleted"
	}
	mu.Unlock()

	if ok && status == "running" {
		if task.Cancel != nil {
			task.Cancel()
		}
		if doneCh != nil {
			select {
			case <-doneCh:
			case <-time.After(10 * time.Second):
			}
		}
	}

	if !ok {
		statePath := latestStateFile(runtimeStatesDir, taskID)
		if statePath == "" {
			statePath = latestStateFile(historyStatesDir, taskID)
		}
		if statePath != "" {
			var state TaskState
			data, err := os.ReadFile(statePath)
			if err == nil && json.Unmarshal(data, &state) == nil {
				normalizeState(&state)
				inputPath = state.InputPath
				outPath = state.OutPath
			}
		}
	}

	deleteTaskFiles(taskID, inputPath, outPath)

	mu.Lock()
	delete(tasks, taskID)
	mu.Unlock()
}

func deleteTaskFiles(taskID, inputPath, outPath string) {
	paths := make(map[string]struct{})
	if inputPath != "" {
		paths[inputPath] = struct{}{}
		paths[inputPath+".state.json"] = struct{}{}
		paths[runtimeStatePath(inputPath)] = struct{}{}
		paths[historyStatePath(inputPath)] = struct{}{}
	}
	if outPath != "" {
		paths[outPath] = struct{}{}
	}
	pattern := filepath.Join("temp_uploads", taskID+"*")
	matches, _ := filepath.Glob(pattern)
	for _, p := range matches {
		paths[p] = struct{}{}
	}
	runtimeMatches, _ := filepath.Glob(filepath.Join(runtimeStatesDir, taskID+"*.state.json"))
	for _, p := range runtimeMatches {
		paths[p] = struct{}{}
	}
	historyMatches, _ := filepath.Glob(filepath.Join(historyStatesDir, taskID+"*.state.json"))
	for _, p := range historyMatches {
		paths[p] = struct{}{}
	}
	for p := range paths {
		os.Remove(p)
	}
}

// modelEntry is one locally available model together with the engine that
// serves it ("omlx"/"mlx"/"ollama") and its recommended chapter batch size.
type modelEntry struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	ChunkSize int    `json:"chunk_size"`
}

// Default local engine endpoints probed in auto-detect mode.
var defaultEngineEndpoints = []string{
	"http://127.0.0.1:8000",  // oMLX (managed MLX server, API-key auth)
	"http://127.0.0.1:8080",  // mlx_lm.server (OpenAI-compatible)
	"http://127.0.0.1:11434", // Ollama
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	apiURL := r.URL.Query().Get("api_url")
	client := &http.Client{Timeout: 3 * time.Second}

	models := []modelEntry{}
	if apiURL != "" {
		models = probeModelsAtURL(client, apiURL)
	} else {
		// Auto-detect: probe the standard local endpoints of every supported
		// engine and merge whatever answers.
		for _, endpoint := range defaultEngineEndpoints {
			models = append(models, probeModelsAtURL(client, endpoint)...)
		}
	}

	detectedEngine := ""
	if len(models) > 0 {
		detectedEngine = models[0].Engine
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":          models,
		"detected_engine": detectedEngine,
	})
}

// probeModelsAtURL lists the models served by one base URL. It tries the
// OpenAI-compatible /v1/models endpoint first (oMLX, mlx_lm.server, LM Studio
// and modern Ollama all speak it), then falls back to Ollama's native
// /api/tags. Endpoints that answer 401 are retried once with the local oMLX
// API key from ~/.omlx/settings.json.
func probeModelsAtURL(client *http.Client, apiURL string) []modelEntry {
	baseURL := "http://127.0.0.1:8000"
	if !strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://") {
		apiURL = "http://" + apiURL
	}
	if parsed, err := url.Parse(apiURL); err == nil && parsed.Host != "" {
		baseURL = parsed.Scheme + "://" + parsed.Host
	}

	if names := fetchModelList(client, baseURL+"/v1/models", config.ReadOMLXAPIKey(), decodeOpenAIModels); len(names) > 0 {
		return tagModelNames(names, engineForBaseURL(baseURL))
	}
	if names := fetchModelList(client, baseURL+"/api/tags", "", decodeOllamaModels); len(names) > 0 {
		return tagModelNames(names, config.EngineOllama)
	}
	return nil
}

// engineForBaseURL labels probed models with the engine that serves them,
// keyed on the well-known local ports.
func engineForBaseURL(baseURL string) string {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, ":11434"):
		return config.EngineOllama
	case strings.Contains(u, ":8000"):
		return config.EngineOmlx
	case strings.Contains(u, ":8080"):
		return config.EngineMLX
	}
	// Custom OpenAI-compatible server.
	return config.EngineMLX
}

func tagModelNames(names []string, engine string) []modelEntry {
	entries := make([]modelEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, modelEntry{
			Name:      n,
			Engine:    engine,
			ChunkSize: config.AutoCalculateMaxChunkSize(n),
		})
	}
	return entries
}

func decodeOpenAIModels(body []byte) ([]string, error) {
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if strings.TrimSpace(m.ID) != "" {
			names = append(names, m.ID)
		}
	}
	return names, nil
}

func decodeOllamaModels(body []byte) ([]string, error) {
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// fetchModelList GETs a model-list endpoint and decodes the names. When the
// server answers 401 and an apiKey is available, the request is retried once
// with the bearer token.
func fetchModelList(client *http.Client, listURL, apiKey string, decode func([]byte) ([]string, error)) []string {
	do := func(key string) (int, []byte, error) {
		req, err := http.NewRequest("GET", listURL, nil)
		if err != nil {
			return 0, nil, err
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, body, err
	}

	status, body, err := do("")
	if err != nil || status != http.StatusOK {
		if status != http.StatusUnauthorized || apiKey == "" {
			return nil
		}
		status, body, err = do(apiKey)
		if err != nil || status != http.StatusOK {
			return nil
		}
	}
	names, err := decode(body)
	if err != nil {
		return nil
	}
	sort.Strings(names)
	return names
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
	if t.DoneCh == nil {
		t.DoneCh = make(chan struct{})
	}
	defer close(t.DoneCh)
	// Re-check the status under the global lock: a delete/pause may have
	// landed between the worker's pickup and this point.
	mu.Lock()
	if t.Status == "deleted" || t.Status == "paused" {
		mu.Unlock()
		return
	}
	t.Status = "running"
	t.StatusReason = ""
	mu.Unlock()
	startTime := time.Now()
	if t.StartedAt.IsZero() {
		t.StartedAt = startTime
	}
	t.LastResumeAt = startTime
	t.InstanceID = instanceID
	t.LastHeartbeat = time.Now()
	if t.Cancel != nil {
		t.Cancel()
	}
	t.Ctx, t.Cancel = context.WithCancel(context.Background())
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
		elapsedSec := currentElapsedSec(t, time.Now())
		etaSec := computeEtaSec(t.Current, t.Total, elapsedSec)
		t.MessageCh <- webtask.LogMsg{
			Type:       mType,
			Message:    msg,
			Total:      t.Total,
			Current:    t.Current,
			Status:     t.Status,
			ElapsedSec: int(elapsedSec),
			EtaSec:     etaSec,
		}
	}

	fail := func(err error) {
		accumulateElapsed(t, time.Now())
		if t.Status == "deleted" {
			// Task was deleted mid-flight; do not resurrect any state.
			return
		}
		t.Status = "error"
		t.StatusReason = "fatal_error"
		t.Error = err.Error()
		saveTaskState(t)
		elapsedSec := currentElapsedSec(t, time.Now())
		etaSec := computeEtaSec(t.Current, t.Total, elapsedSec)
		t.MessageCh <- webtask.LogMsg{
			Type:       "red",
			Message:    fmt.Sprintf("❌ 发生严重错误: %v", err),
			Status:     "error",
			ElapsedSec: int(elapsedSec),
			EtaSec:     etaSec,
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

	engineName := config.EngineLabel(t.Config.Engine)
	sendLog(fmt.Sprintf("引擎已启动 (%s, 并发 = %d)。章节上下文批处理模式：同一章节的段落合并为批次翻译，并携带章节标题与前文滚动上下文。", engineName, t.Config.Concurrency), "gray")
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

		elapsedSec := currentElapsedSec(t, time.Now())
		etaSec := computeEtaSec(t.Current, total, elapsedSec)
		t.MessageCh <- webtask.LogMsg{
			Type:       mType,
			Message:    msg,
			Total:      total,
			Current:    t.Current,
			Status:     t.Status,
			ElapsedSec: int(elapsedSec),
			EtaSec:     etaSec,
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
			mu.Lock()
			stopExport := t.StopExportRequested
			deletedMidFlight := t.Status == "deleted"
			mu.Unlock()
			if stopExport && !deletedMidFlight {
				translatedCount, total, aerr := buildPartialOutput(t, p, blocks)
				if aerr != nil {
					fail(aerr)
					return
				}
				finishPartialExport(t, translatedCount, total)
				return
			}
			accumulateElapsed(t, time.Now())
			if t.Status != "deleted" {
				t.Status = "paused"
				t.StatusReason = "paused_by_user"
			}
			saveTaskState(t)
			elapsedSec := currentElapsedSec(t, time.Now())
			etaSec := computeEtaSec(t.Current, t.Total, elapsedSec)
			safeSendTaskMessage(t, webtask.LogMsg{
				Type:       "orange",
				Message:    "任务已暂停",
				Status:     "paused",
				ElapsedSec: int(elapsedSec),
				EtaSec:     etaSec,
			})
			return
		}
		fail(err)
		return
	}

	t.Current = t.Total // Hack to show 100% since we executed in batch mode internally
	sendLog("所有块翻译完毕。汇编构建输出文件...", "gray")

	mu.Lock()
	deletedMidFlight := t.Status == "deleted"
	mu.Unlock()
	if deletedMidFlight {
		return
	}

	err = p.Assemble(translatedBlocks, t.Config.OutputFile, t.Config.Bilingual)
	if err != nil {
		fail(err)
		return
	}

	accumulateElapsed(t, time.Now())
	t.Stats = stats
	t.Status = "completed"
	t.StatusReason = ""
	saveTaskState(t)
	// State is saved after the output is assembled, so bump the output's
	// mtime: download uses "state newer than output" as the signal that the
	// state was hand-edited and the output needs rebuilding.
	if now := time.Now(); t.OutPath != "" {
		os.Chtimes(t.OutPath, now, now)
	}
	sendLog(fmt.Sprintf("📊 翻译统计: 成功=%d 术语降级=%d 拒答=%d 完全失败=%d", stats.SuccessCount, stats.FallbackCount, stats.RefusedCount, stats.FailureCount), "gray")
	sendLog("🎉 生成最终电子书/文档成功！", "green")
	elapsed := time.Duration(currentElapsedSec(t, time.Now())) * time.Second
	sendLog(fmt.Sprintf("⏱️ 翻译总耗时: %s", formatDuration(elapsed)), "green")

	elapsedSec := currentElapsedSec(t, time.Now())
	etaSec := computeEtaSec(t.Current, t.Total, elapsedSec)
	t.MessageCh <- webtask.LogMsg{
		Status:     "completed",
		Total:      t.Total,
		Current:    t.Total,
		ElapsedSec: int(elapsedSec),
		EtaSec:     etaSec,
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
		if task.Status == "paused" || task.Status == "deleted" {
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
