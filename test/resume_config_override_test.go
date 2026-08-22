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

// TestResumeWithConfigOverride verifies that /api/resume accepts a config
// JSON body and applies it to the resumed task, so the user can pause a
// task, switch the local model in the UI, and resume with the new model.
func TestResumeWithConfigOverride(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_resume_override"
	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	historyDir := filepath.Join(baseDir, "history_states")
	os.MkdirAll(historyDir, 0755)

	// Input file intentionally missing so the resumed worker fails fast
	// (parser cannot extract) and saves state with the overridden config.
	input := filepath.Join(baseDir, taskID+".txt")
	output := filepath.Join(baseDir, taskID+"_translated.txt")
	statePath := filepath.Join(historyDir, filepath.Base(input)+".state.json")

	state := map[string]interface{}{
		"id":                taskID,
		"status":            "paused",
		"status_reason":     "paused_by_user",
		"input_path":        input,
		"out_path":          output,
		"total":             10,
		"current":           2,
		"last_heartbeat_ts": time.Now().Unix(),
		"src_file_name":     "resume_override.txt",
		"config": map[string]interface{}{
			"api_url":        "http://127.0.0.1:8000/v1",
			"engine":         "omlx",
			"model":          "Old-Model-27B",
			"prompt":         "translate",
			"prompt_role":    "金融翻译专家",
			"max_chunk_size": 1200,
		},
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	// Resume with a brand-new model + engine from the UI.
	body := `{
		"engine": "omlx",
		"model": "Hy-MT2-1.8B-4bit",
		"api_url": "http://127.0.0.1:8000/v1",
		"prompt_role": "金融翻译专家",
		"bilingual": true
	}`
	req, _ := http.NewRequest("POST", srv.BaseURL+"/api/resume?task_id="+taskID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resume request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on resume, got %d", resp.StatusCode)
	}

	// The worker fails fast on the missing input file; wait for the error
	// state (which persists the overridden config).
	deadline := time.Now().Add(15 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		sresp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
		if err == nil {
			var st map[string]interface{}
			json.NewDecoder(sresp.Body).Decode(&st)
			sresp.Body.Close()
			if s, ok := st["status"].(string); ok {
				status = s
				if status == "error" {
					break
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status != "error" {
		t.Fatalf("expected resumed task to fail on missing input, got status %q", status)
	}

	// Read the freshest state file and assert the override was persisted.
	newest := ""
	var newestMod time.Time
	for _, dir := range []string{historyDir, filepath.Join(baseDir, "runtime_states")} {
		matches, _ := filepath.Glob(filepath.Join(dir, taskID+"*.state.json"))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && (newest == "" || st.ModTime().After(newestMod)) {
				newest, newestMod = m, st.ModTime()
			}
		}
	}
	if newest == "" {
		t.Fatalf("no state file found after resume")
	}
	raw, err := os.ReadFile(newest)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var final map[string]interface{}
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	cfg, _ := final["config"].(map[string]interface{})
	if cfg == nil {
		t.Fatalf("state has no config: %s", string(raw))
	}
	if got := fmt.Sprint(cfg["model"]); got != "Hy-MT2-1.8B-4bit" {
		t.Fatalf("expected model override, got %q", got)
	}
	if got := fmt.Sprint(cfg["engine"]); got != "omlx" {
		t.Fatalf("expected engine override, got %q", got)
	}
	// Batch size was omitted from the request: keep the original so
	// completed-chunk keys stay aligned across the resume.
	if got, _ := cfg["max_chunk_size"].(float64); int(got) != 1200 {
		t.Fatalf("expected original max_chunk_size 1200 to be kept, got %v", cfg["max_chunk_size"])
	}
}
