package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedHistoryTask(t *testing.T, srv *TestServer, taskID string) (input, output, statePath string) {
	t.Helper()
	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	historyDir := filepath.Join(baseDir, "history_states")
	os.MkdirAll(historyDir, 0755)

	input = filepath.Join(baseDir, taskID+".txt")
	output = filepath.Join(baseDir, taskID+"_translated.txt")
	statePath = filepath.Join(historyDir, filepath.Base(input)+".state.json")

	os.WriteFile(input, []byte("dummy "+taskID), 0644)
	os.WriteFile(output, []byte("translated "+taskID), 0644)
	state := map[string]interface{}{
		"id":                taskID,
		"status":            "completed",
		"input_path":        input,
		"out_path":          output,
		"total":             3,
		"current":           3,
		"last_heartbeat_ts": time.Now().Unix(),
		"src_file_name":     taskID + ".txt",
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)
	return
}

// TestTasksBatchDelete verifies DELETE /api/tasks with explicit ids removes
// exactly the selected tasks and their files.
func TestTasksBatchDelete(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	in1, out1, st1 := seedHistoryTask(t, srv, "task_batch_1")
	in2, _, st2 := seedHistoryTask(t, srv, "task_batch_2")
	in3, _, st3 := seedHistoryTask(t, srv, "task_batch_3")

	body := `{"ids": ["task_batch_1", "task_batch_2"]}`
	req, _ := http.NewRequest("DELETE", srv.BaseURL+"/api/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch delete failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	if deleted, _ := res["deleted"].(float64); int(deleted) != 2 {
		t.Fatalf("expected 2 deleted, got %v (failed=%v)", res["deleted"], res["failed"])
	}

	for _, p := range []string{in1, out1, st1, in2, st2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s should be deleted", p)
		}
	}
	// Unselected task survives.
	if _, err := os.Stat(in3); err != nil {
		t.Fatalf("task_batch_3 input should survive: %v", err)
	}
	if _, err := os.Stat(st3); err != nil {
		t.Fatalf("task_batch_3 state should survive: %v", err)
	}
}

// TestTasksClearAll verifies DELETE /api/tasks without a body removes every
// known task, leaving an empty history list.
func TestTasksClearAll(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	seedHistoryTask(t, srv, "task_clear_1")
	seedHistoryTask(t, srv, "task_clear_2")

	// How many tasks are currently listed?
	resp, err := http.Get(srv.BaseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("list tasks failed: %v", err)
	}
	var list struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Tasks) == 0 {
		t.Fatalf("expected seeded tasks to be listed")
	}

	req, _ := http.NewRequest("DELETE", srv.BaseURL+"/api/tasks", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("clear-all failed: %v", err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", dresp.StatusCode)
	}
	var res map[string]interface{}
	json.NewDecoder(dresp.Body).Decode(&res)
	if deleted, _ := res["deleted"].(float64); int(deleted) < len(list.Tasks) {
		t.Fatalf("expected at least %d deleted, got %v", len(list.Tasks), res["deleted"])
	}

	// History must now be empty.
	resp2, err := http.Get(srv.BaseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("list tasks failed: %v", err)
	}
	var list2 struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	json.NewDecoder(resp2.Body).Decode(&list2)
	resp2.Body.Close()
	if len(list2.Tasks) != 0 {
		remaining := make([]string, 0, len(list2.Tasks))
		for _, task := range list2.Tasks {
			remaining = append(remaining, fmt.Sprint(task["id"]))
		}
		t.Fatalf("expected empty task list after clear-all, got %v", remaining)
	}

	// State directories must be empty as well.
	for _, dir := range []string{"history_states", "runtime_states"} {
		matches, _ := filepath.Glob(filepath.Join(srv.WorkDir, "temp_uploads", dir, "*.state.json"))
		if len(matches) != 0 {
			t.Fatalf("expected %s to be empty, found %d files", dir, len(matches))
		}
	}
}
