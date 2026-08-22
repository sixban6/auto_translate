package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func slowTranslateEngine(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		text := ""
		for _, m := range payload.Messages {
			if m.Role == "user" {
				text = m.Content
				break
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[T]" + text}},
			},
		})
	}))
}

func stopExportTestBook() string {
	var sb bytes.Buffer
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&sb, "Paragraph number %d carries some unique content %d inside it.\n\n", i, i*7)
	}
	return sb.String()
}

func startStopExportTask(t *testing.T, srv *TestServer, engineURL, content string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	cfg := map[string]interface{}{
		"api_url":             engineURL,
		"engine":              "mlx",
		"model":               "dummy",
		"temperature":         0.1,
		"max_chunk_size":      220,
		"concurrency":         1,
		"max_retries":         1,
		"request_timeout_sec": 30,
		"prompt":              "Translate the text to another language.",
		"bilingual":           false,
	}
	cfgJSON, _ := json.Marshal(cfg)
	fw, _ := mw.CreateFormField("config")
	fw.Write(cfgJSON)
	ff, _ := mw.CreateFormFile("file", "book.txt")
	ff.Write([]byte(content))
	mw.Close()

	resp, err := http.Post(srv.BaseURL+"/api/translate", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("start task status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.TaskID == "" {
		t.Fatal("empty task_id")
	}
	return out.TaskID
}

func taskStatusMap(t *testing.T, srv *TestServer, taskID string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(srv.BaseURL + "/api/task_status?task_id=" + taskID)
	if err != nil {
		t.Fatalf("task_status: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&payload)
	return payload
}

func waitTaskStatus(t *testing.T, srv *TestServer, taskID, want string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		payload := taskStatusMap(t, srv, taskID)
		if payload["status"] == want {
			return payload
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("task %s never reached status %q", taskID, want)
	return nil
}

func completedChunkCount(t *testing.T, srv *TestServer, taskID string) int {
	t.Helper()
	statePath := filepath.Join(srv.WorkDir, "temp_uploads", "runtime_states", taskID+".txt.state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return 0
	}
	var state struct {
		CompletedChunks map[string]string `json:"completed_chunks"`
	}
	if json.Unmarshal(data, &state) != nil {
		return 0
	}
	return len(state.CompletedChunks)
}

func cleanupStopExportTask(srv *TestServer, taskID string) {
	base := filepath.Join(srv.WorkDir, "temp_uploads")
	for _, rel := range []string{
		taskID + ".txt",
		taskID + "_translated.txt",
		filepath.Join("runtime_states", taskID+".txt.state.json"),
		filepath.Join("history_states", taskID+".txt.state.json"),
	} {
		os.Remove(filepath.Join(base, rel))
	}
}

// TestStopExport_RunningTask terminates a task mid-translation and expects a
// downloadable output containing both translated and untranslated paragraphs.
func TestStopExport_RunningTask(t *testing.T) {
	engine := slowTranslateEngine(700 * time.Millisecond)
	defer engine.Close()
	srv := startServer(t)
	defer srv.Close()

	taskID := startStopExportTask(t, srv, engine.URL, stopExportTestBook())
	defer cleanupStopExportTask(srv, taskID)

	// Wait until at least one batch has been translated and persisted.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if completedChunkCount(t, srv, taskID) >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if completedChunkCount(t, srv, taskID) == 0 {
		t.Fatal("no batch completed before stop; engine too slow?")
	}

	resp, err := http.Post(srv.BaseURL+"/api/stop_export?task_id="+taskID, "", nil)
	if err != nil {
		t.Fatalf("stop_export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stop_export status %d: %s", resp.StatusCode, body)
	}
	var out map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "stopping" {
		t.Fatalf("expected stopping, got %v", out)
	}

	payload := waitTaskStatus(t, srv, taskID, "completed", 30*time.Second)
	if payload["status_reason"] != "stopped_partial" {
		t.Fatalf("expected stopped_partial reason, got %v", payload["status_reason"])
	}

	dl, err := http.Get(srv.BaseURL + "/api/download?task_id=" + taskID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", dl.StatusCode)
	}
	body, _ := io.ReadAll(dl.Body)
	content := string(body)
	if got := bytes.Count(body, []byte("[T]")); got == 0 || got >= 8 {
		t.Errorf("expected a partial translation (some but not all paragraphs), got %d translated", got)
	}
	if !bytes.Contains(body, []byte("Paragraph number")) {
		t.Errorf("untranslated paragraphs must keep their original text: %q", content)
	}
}

// TestStopExport_PausedTask exports inline for a paused task.
func TestStopExport_PausedTask(t *testing.T) {
	engine := slowTranslateEngine(5 * time.Second)
	defer engine.Close()
	srv := startServer(t)
	defer srv.Close()

	taskID := startStopExportTask(t, srv, engine.URL, stopExportTestBook())
	defer cleanupStopExportTask(srv, taskID)

	waitTaskStatus(t, srv, taskID, "running", 20*time.Second)

	pauseResp, err := http.Post(srv.BaseURL+"/api/pause?task_id="+taskID, "", nil)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	pauseResp.Body.Close()
	waitTaskStatus(t, srv, taskID, "paused", 20*time.Second)

	resp, err := http.Post(srv.BaseURL+"/api/stop_export?task_id="+taskID, "", nil)
	if err != nil {
		t.Fatalf("stop_export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stop_export status %d: %s", resp.StatusCode, body)
	}
	var out map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "completed" {
		t.Fatalf("expected inline completed export, got %v", out)
	}
	if total, _ := out["total"].(float64); total != 8 {
		t.Errorf("expected 8 total blocks, got %v", out["total"])
	}

	payload := taskStatusMap(t, srv, taskID)
	if payload["status_reason"] != "stopped_partial" {
		t.Fatalf("expected stopped_partial reason, got %v", payload["status_reason"])
	}

	dl, err := http.Get(srv.BaseURL + "/api/download?task_id=" + taskID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dl.Body.Close()
	body, _ := io.ReadAll(dl.Body)
	if dl.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Paragraph number 1")) {
		t.Fatalf("paused export must contain original paragraphs, status=%d", dl.StatusCode)
	}
}
