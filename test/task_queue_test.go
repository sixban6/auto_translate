package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestTaskConcurrency_LimitEnforcement(t *testing.T) {
	os.Setenv("TRANSLATE_MAX_PARALLEL", "1")
	defer os.Unsetenv("TRANSLATE_MAX_PARALLEL")

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

	postTask := func() string {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 200, "concurrency": 1, "api_url": "` + llm.URL + `"}`
		writer.WriteField("config", cfg)
		part, _ := writer.CreateFormFile("file", "test_queue.txt")
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
		return res["task_id"]
	}

	task1 := postTask()
	task2 := postTask()
	task3 := postTask()

	time.Sleep(200 * time.Millisecond)

	resp, err := http.Get(srv.BaseURL + "/api/tasks")
	if err != nil {
		t.Fatalf("tasks request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Tasks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	statusMap := map[string]string{}
	for _, tsk := range payload.Tasks {
		statusMap[tsk.ID] = tsk.Status
	}
	if statusMap[task1] != "running" {
		t.Fatalf("expected task1 running, got %s", statusMap[task1])
	}
	if statusMap[task2] != "queued" {
		t.Fatalf("expected task2 queued, got %s", statusMap[task2])
	}
	if statusMap[task3] != "queued" {
		t.Fatalf("expected task3 queued, got %s", statusMap[task3])
	}
}
