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
	statePath := inputPath + ".state.json"
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
	for _, task := range payload.Tasks {
		if task["id"] == taskID {
			if task["eta_sec"].(float64) != -1 {
				t.Fatalf("expected eta_sec -1, got %v", task["eta_sec"])
			}
		}
	}
}

func TestETA_OnResume_SmoothDecay(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_eta_resume"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".txt")
	statePath := inputPath + ".state.json"
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
	for _, task := range payload.Tasks {
		if task["id"] == taskID {
			eta := int(task["eta_sec"].(float64))
			if eta < 60 {
				t.Fatalf("expected eta_sec not too small, got %d", eta)
			}
		}
	}
}
