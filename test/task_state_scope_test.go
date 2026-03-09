package test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateScope_RuntimeNotInHistoryList(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	runtimeDir := filepath.Join(baseDir, "runtime_states")
	historyDir := filepath.Join(baseDir, "history_states")
	os.MkdirAll(runtimeDir, 0755)
	os.MkdirAll(historyDir, 0755)

	runtimeID := "task_runtime_only"
	historyID := "task_history_only"

	runtimeInput := filepath.Join(baseDir, runtimeID+".txt")
	historyInput := filepath.Join(baseDir, historyID+".txt")
	os.WriteFile(runtimeInput, []byte("dummy"), 0644)
	os.WriteFile(historyInput, []byte("dummy"), 0644)
	defer os.Remove(runtimeInput)
	defer os.Remove(historyInput)

	runtimeState := map[string]interface{}{
		"id":                runtimeID,
		"status":            "running",
		"input_path":        runtimeInput,
		"out_path":          runtimeInput + "_translated.txt",
		"last_heartbeat_ts": time.Now().Unix(),
	}
	historyState := map[string]interface{}{
		"id":                historyID,
		"status":            "completed",
		"input_path":        historyInput,
		"out_path":          historyInput + "_translated.txt",
		"last_heartbeat_ts": time.Now().Unix(),
	}
	runtimePath := filepath.Join(runtimeDir, filepath.Base(runtimeInput)+".state.json")
	historyPath := filepath.Join(historyDir, filepath.Base(historyInput)+".state.json")
	data1, _ := json.MarshalIndent(runtimeState, "", "  ")
	data2, _ := json.MarshalIndent(historyState, "", "  ")
	os.WriteFile(runtimePath, data1, 0644)
	os.WriteFile(historyPath, data2, 0644)
	defer os.Remove(runtimePath)
	defer os.Remove(historyPath)

	resp, err := http.Get(srv.BaseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("tasks request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	foundRuntime := false
	foundHistory := false
	for _, tsk := range payload.Tasks {
		if tsk["id"] == runtimeID {
			foundRuntime = true
		}
		if tsk["id"] == historyID {
			foundHistory = true
		}
	}
	if foundRuntime {
		t.Fatalf("runtime task should not appear in history list")
	}
	if !foundHistory {
		t.Fatalf("history task should appear in history list")
	}
}
