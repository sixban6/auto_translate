package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResumeSSEStatus(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 20, "concurrency": 1}`
	writer.WriteField("config", cfg)

	part, _ := writer.CreateFormFile("file", "test_sse.txt")
	text := strings.Repeat("Another sentence to test progress resuming. ", 50)
	part.Write([]byte(text))
	writer.Close()

	req, _ := http.NewRequest("POST", srv.BaseURL+"/api/translate", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{Timeout: 5 * time.Second}
	resp, _ := client.Do(req)
	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	taskID := res["task_id"]

	time.Sleep(2 * time.Second)

	srv.Close()
	srv2 := startServer(t)
	defer srv2.Close()

	reqRes, _ := http.NewRequest("POST", srv2.BaseURL+"/api/resume?task_id="+taskID, nil)
	respRes, _ := client.Do(reqRes)
	respRes.Body.Close()

	reqSSE, _ := http.NewRequest("GET", srv2.BaseURL+"/api/progress?task_id="+taskID, nil)
	respSSE, err := client.Do(reqSSE)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer respSSE.Body.Close()

	reader := bufio.NewReader(respSSE.Body)
	timeout := time.After(30 * time.Second)

	initialProgressFound := false

	for {
		select {
		case <-timeout:
			t.Fatalf("Timeout waiting for completion")
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				var data map[string]interface{}
				json.Unmarshal([]byte(line[6:]), &data)

				if currentFloat, ok := data["current"].(float64); ok && currentFloat > 0 {
					initialProgressFound = true
				}

				if data["status"] == "completed" {
					if !initialProgressFound {
						t.Errorf("Progress counter current did not advance")
					}
					return
				}
			}
		}
	}
}
