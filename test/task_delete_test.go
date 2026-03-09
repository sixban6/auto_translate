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

func TestTaskDelete_RemovesAllArtifacts(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	taskID := "task_delete_artifacts"
	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	os.MkdirAll(baseDir, 0755)

	input := filepath.Join(baseDir, taskID+".txt")
	output := filepath.Join(baseDir, taskID+"_translated.txt")
	statePath := input + ".state.json"

	os.WriteFile(input, []byte("dummy"), 0644)
	os.WriteFile(output, []byte("translated"), 0644)
	state := map[string]interface{}{
		"id":                taskID,
		"status":            "completed",
		"input_path":        input,
		"out_path":          output,
		"last_heartbeat_ts": time.Now().Unix(),
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	req, _ := http.NewRequest("DELETE", srv.BaseURL+"/api/tasks/"+taskID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("input should be deleted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output should be deleted")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state should be deleted")
	}
}

func TestTaskDelete_CancelRunningTaskFirst(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
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
	cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 200, "concurrency": 1, "api_url": "` + llm.URL + `"}`
	writer.WriteField("config", cfg)
	part, _ := writer.CreateFormFile("file", "delete_running.txt")
	part.Write([]byte("This is a long line that should take some time to finish."))
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

	reqDel, _ := http.NewRequest("DELETE", srv.BaseURL+"/api/tasks/"+taskID, nil)
	delResp, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	delResp.Body.Close()

	baseDir := filepath.Join(srv.WorkDir, "temp_uploads")
	pattern := filepath.Join(baseDir, taskID+"*")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(pattern)
		if len(matches) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("artifacts still exist for %s", taskID)
}
