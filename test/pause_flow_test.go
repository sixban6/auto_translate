package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPause_CancelsTranslation(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
		}
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

	part, _ := writer.CreateFormFile("file", "test_pause.txt")
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
	if taskID == "" {
		t.Fatalf("missing task_id")
	}

	time.Sleep(500 * time.Millisecond)

	pauseReq, _ := http.NewRequest("POST", srv.BaseURL+"/api/pause?task_id="+taskID, nil)
	pauseResp, err := http.DefaultClient.Do(pauseReq)
	if err != nil {
		t.Fatalf("pause request failed: %v", err)
	}
	pauseResp.Body.Close()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for paused status")
		default:
			statResp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
			if err != nil {
				t.Fatalf("status request failed: %v", err)
			}
			var stat map[string]interface{}
			json.NewDecoder(statResp.Body).Decode(&stat)
			statResp.Body.Close()
			if stat["status"] == "paused" {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}
