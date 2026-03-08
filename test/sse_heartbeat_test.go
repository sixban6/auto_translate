package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSSEHeartbeat(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 100, "concurrency": 1}`
	writer.WriteField("config", cfg)

	part, _ := writer.CreateFormFile("file", "test.txt")
	// Make it long enough that it takes over 5 seconds (heartbeat interval) to process
	part.Write([]byte(strings.Repeat("Hello world. This is a very very long test file array set for auto translate pipeline validation. ", 50)))
	writer.Close()

	req, _ := http.NewRequest("POST", srv.BaseURL+"/api/translate", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Translate POST failed: %v", err)
	}
	defer resp.Body.Close()

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	taskID := res["task_id"]
	if taskID == "" {
		t.Fatalf("No task_id returned")
	}

	reqSSE, _ := http.NewRequest("GET", srv.BaseURL+"/api/progress?task_id="+taskID, nil)
	clientSSE := &http.Client{}
	respSSE, err := clientSSE.Do(reqSSE)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer respSSE.Body.Close()

	reader := bufio.NewReader(respSSE.Body)
	heartbeatFound := false
	timeout := time.After(8 * time.Second)

	for {
		select {
		case <-timeout:
			if !heartbeatFound {
				t.Fatalf("Did not receive heartbeat within 8 seconds")
			}
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF && heartbeatFound {
					return
				}
				if heartbeatFound {
					return
				}
				t.Fatalf("Read SSE error: %v", err)
			}
			if strings.Contains(line, "heartbeat") {
				heartbeatFound = true
			}
			if strings.Contains(line, `"status":"completed"`) && heartbeatFound {
				return
			}
		}
	}
}
