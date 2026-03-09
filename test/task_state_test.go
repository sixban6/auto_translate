package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskState_PersistSrcFileName(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respMap := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": "OK",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(respMap)
	}))
	defer llm.Close()

	srv := startServer(t)
	defer srv.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 50, "concurrency": 1, "api_url": "` + llm.URL + `"}`
	writer.WriteField("config", cfg)
	part, _ := writer.CreateFormFile("file", "original_upload_name.txt")
	part.Write([]byte("hello world. this is a short text for testing."))
	writer.Close()

	req, _ := http.NewRequest("POST", srv.BaseURL+"/api/translate", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("translate request failed: %v", err)
	}
	defer resp.Body.Close()
	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	taskID := res["task_id"]

	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	statePath := filepath.Join(baseDir, taskID+".txt.state.json")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read state: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal state failed: %v", err)
	}
	if payload["src_file_name"] != "original_upload_name.txt" {
		t.Fatalf("expected src_file_name original_upload_name.txt, got %v", payload["src_file_name"])
	}
}

func TestTaskState_ResumeKeepsElapsed(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_elapsed_resume"
	inputPath := filepath.Join(srv.WorkDir, "temp_uploads", taskID+".txt")
	statePath := inputPath + ".state.json"
	os.WriteFile(inputPath, []byte("dummy"), 0644)
	defer os.Remove(inputPath)
	defer os.Remove(statePath)

	state := map[string]interface{}{
		"id":                     taskID,
		"status":                 "paused",
		"input_path":             inputPath,
		"out_path":               inputPath + "_translated.txt",
		"current":                50,
		"total":                  100,
		"last_heartbeat_ts":      time.Now().Unix(),
		"elapsed_sec_accumulated": 120,
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	reqResume, _ := http.NewRequest("POST", srv.BaseURL+"/api/resume?task_id="+taskID, nil)
	respResume, err := http.DefaultClient.Do(reqResume)
	if err != nil {
		t.Fatalf("resume request failed: %v", err)
	}
	respResume.Body.Close()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
		if err == nil {
			var payload map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			if payload["elapsed_sec"].(float64) >= 120 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("elapsed not preserved on resume")
}
