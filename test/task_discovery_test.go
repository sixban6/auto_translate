package test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type taskSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	SrcFileName string `json:"src_file_name"`
}

func TestTaskDiscovery_ListAll(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	historyDir := filepath.Join(baseDir, "history_states")
	os.MkdirAll(historyDir, 0755)

	createState := func(id, status string, modTime time.Time) string {
		input := filepath.Join(baseDir, id+".txt")
		statePath := filepath.Join(historyDir, filepath.Base(input)+".state.json")
		os.WriteFile(input, []byte("dummy"), 0644)
		state := map[string]interface{}{
			"id":                id,
			"status":            status,
			"input_path":        input,
			"out_path":          input + "_translated.txt",
			"src_file_name":     id + "_orig.txt",
			"last_heartbeat_ts": time.Now().Unix(),
		}
		data, _ := json.MarshalIndent(state, "", "  ")
		os.WriteFile(statePath, data, 0644)
		os.Chtimes(statePath, modTime, modTime)
		return statePath
	}

	now := time.Now()
	state1 := createState("task_done", "completed", now.Add(-1*time.Minute))
	state2 := createState("task_interrupted", "interrupted", now.Add(-2*time.Minute))
	state3 := createState("task_queued", "queued", now.Add(-3*time.Minute))
	dirtyPath := filepath.Join(historyDir, "dirty.state.json")
	os.WriteFile(dirtyPath, []byte("not-json"), 0644)

	defer os.Remove(state1)
	defer os.Remove(state2)
	defer os.Remove(state3)
	defer os.Remove(dirtyPath)
	defer os.Remove(filepath.Join(baseDir, "task_done.txt"))
	defer os.Remove(filepath.Join(baseDir, "task_interrupted.txt"))
	defer os.Remove(filepath.Join(baseDir, "task_queued.txt"))

	resp, err := http.Get(srv.BaseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("tasks request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Tasks []taskSummary `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	statusMap := map[string]string{}
	nameMap := map[string]string{}
	for _, tsk := range payload.Tasks {
		statusMap[tsk.ID] = tsk.Status
		nameMap[tsk.ID] = tsk.SrcFileName
	}
	if len(statusMap) < 3 {
		t.Fatalf("expected at least 3 tasks, got %d", len(statusMap))
	}
	if statusMap["task_done"] != "completed" {
		t.Fatalf("expected task_done completed, got %s", statusMap["task_done"])
	}
	if statusMap["task_interrupted"] != "interrupted" {
		t.Fatalf("expected task_interrupted interrupted, got %s", statusMap["task_interrupted"])
	}
	if statusMap["task_queued"] != "queued" {
		t.Fatalf("expected task_queued queued, got %s", statusMap["task_queued"])
	}
	if nameMap["task_done"] != "task_done_orig.txt" {
		t.Fatalf("expected src_file_name task_done_orig.txt, got %s", nameMap["task_done"])
	}
}
