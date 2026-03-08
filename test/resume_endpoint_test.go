package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResumeEndpoint(t *testing.T) {
	srv := startServer(t)
	defer srv.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	cfg := `{"prompt_role": "金融翻译专家", "max_chunk_size": 20, "concurrency": 1}`
	writer.WriteField("config", cfg)

	part, _ := writer.CreateFormFile("file", "test_resume.txt")
	text := strings.Repeat("This is a test block that should be interrupted. ", 50)
	part.Write([]byte(text))
	writer.Close()

	req, _ := http.NewRequest("POST", srv.BaseURL+"/api/translate", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Translate POST failed: %v", err)
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	taskID := res["task_id"]

	// Wait 2 seconds for state to generate
	time.Sleep(2 * time.Second)

	// Kill and restart server
	srv.Close()
	srv2 := startServer(t)
	defer srv2.Close()

	respStat, err := client.Get(srv2.BaseURL + "/api/task_status?task_id=" + taskID)
	if err != nil {
		t.Fatalf("Status GET failed: %v", err)
	}
	var stat map[string]interface{}
	json.NewDecoder(respStat.Body).Decode(&stat)
	respStat.Body.Close()

	if stat["resume_supported"] != true {
		t.Errorf("Expected resume_supported to be true, got %v", stat["resume_supported"])
	}

	reqRes, _ := http.NewRequest("POST", srv2.BaseURL+"/api/resume?task_id="+taskID, nil)
	respRes, err := client.Do(reqRes)
	if err != nil {
		t.Fatalf("Resume POST failed: %v", err)
	}
	defer respRes.Body.Close()

	if respRes.StatusCode != 200 {
		t.Errorf("Resume failed with code %d", respRes.StatusCode)
	}

	var res2 map[string]string
	json.NewDecoder(respRes.Body).Decode(&res2)
	if res2["status"] != "resumed" {
		t.Errorf("Expected status resumed, got %v", res2["status"])
	}
}
