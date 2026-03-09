package test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type taskStatePayload struct {
	ID              string `json:"id"`
	Total           int    `json:"total"`
	Current         int    `json:"current"`
	Status          string `json:"status"`
	InputPath       string `json:"input_path"`
	OutPath         string `json:"out_path"`
	InstanceID      string `json:"instance_id"`
	LastHeartbeatTs int64  `json:"last_heartbeat_ts"`
	StatusReason    string `json:"status_reason"`
}

func TestTaskStatus_GhostProcessRejection(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_ghost"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".epub")
	runtimeDir := filepath.Join(srv.WorkDir, "temp_uploads", "runtime_states")
	os.MkdirAll(runtimeDir, 0755)
	statePath := filepath.Join(runtimeDir, filepath.Base(inputPath)+".state.json")
	os.WriteFile(inputPath, []byte("dummy"), 0644)
	defer os.Remove(inputPath)
	defer os.Remove(statePath)

	state := taskStatePayload{
		ID:              taskID,
		Status:          "running",
		InputPath:       inputPath,
		OutPath:         inputPath + "_translated.epub",
		InstanceID:      "old-dead-123",
		LastHeartbeatTs: time.Now().Unix(),
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
	if payload["status"] != "interrupted" {
		t.Fatalf("expected interrupted status, got %v", payload["status"])
	}
	if payload["resume_supported"] != true {
		t.Fatalf("expected resume_supported true, got %v", payload["resume_supported"])
	}
}

func TestTaskStatus_QueuedGhostRejection(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_queued_ghost"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".epub")
	runtimeDir := filepath.Join(srv.WorkDir, "temp_uploads", "runtime_states")
	os.MkdirAll(runtimeDir, 0755)
	statePath := filepath.Join(runtimeDir, filepath.Base(inputPath)+".state.json")
	os.WriteFile(inputPath, []byte("dummy"), 0644)
	defer os.Remove(inputPath)
	defer os.Remove(statePath)

	state := taskStatePayload{
		ID:              taskID,
		Status:          "queued",
		InputPath:       inputPath,
		OutPath:         inputPath + "_translated.epub",
		InstanceID:      "old-dead-queued",
		LastHeartbeatTs: time.Now().Unix(),
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
	if payload["status"] != "interrupted" {
		t.Fatalf("expected interrupted status, got %v", payload["status"])
	}
	if payload["resume_supported"] != true {
		t.Fatalf("expected resume_supported true, got %v", payload["resume_supported"])
	}
}
