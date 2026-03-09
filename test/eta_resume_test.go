package test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestETA_Calculation_WithZeroCurrent(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_eta_zero"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".txt")
	runtimeDir := filepath.Join(srv.WorkDir, "temp_uploads", "runtime_states")
	os.MkdirAll(runtimeDir, 0755)
	statePath := filepath.Join(runtimeDir, filepath.Base(inputPath)+".state.json")
	os.WriteFile(inputPath, []byte("dummy"), 0644)
	defer os.Remove(inputPath)
	defer os.Remove(statePath)

	state := map[string]interface{}{
		"id":                     taskID,
		"status":                 "running",
		"input_path":             inputPath,
		"out_path":               inputPath + "_translated.txt",
		"current":                0,
		"total":                  100,
		"last_heartbeat_ts":      time.Now().Unix(),
		"last_resume_at":         time.Now().Unix(),
		"elapsed_sec_accumulated": 0,
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	resp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if payload["eta_sec"].(float64) != -1 {
		t.Fatalf("expected eta_sec -1, got %v", payload["eta_sec"])
	}
}

func TestETA_OnResume_SmoothDecay(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_eta_resume"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".txt")
	runtimeDir := filepath.Join(srv.WorkDir, "temp_uploads", "runtime_states")
	os.MkdirAll(runtimeDir, 0755)
	statePath := filepath.Join(runtimeDir, filepath.Base(inputPath)+".state.json")
	os.WriteFile(inputPath, []byte("dummy"), 0644)
	defer os.Remove(inputPath)
	defer os.Remove(statePath)

	state := map[string]interface{}{
		"id":                     taskID,
		"status":                 "running",
		"input_path":             inputPath,
		"out_path":               inputPath + "_translated.txt",
		"current":                50,
		"total":                  100,
		"last_heartbeat_ts":      time.Now().Unix(),
		"last_resume_at":         time.Now().Unix(),
		"elapsed_sec_accumulated": 100,
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	resp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	eta := int(payload["eta_sec"].(float64))
	if eta < 60 {
		t.Fatalf("expected eta_sec not too small, got %d", eta)
	}
}
