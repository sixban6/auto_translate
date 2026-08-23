package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// submitTranslateTask uploads a txt task via the API and returns its id.
func submitTranslateTask(t *testing.T, srvBase, apiURL, text string) string {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	cfg := `{"prompt": "p", "max_chunk_size": 200, "concurrency": 1, "api_url": "` + apiURL + `"}`
	writer.WriteField("config", cfg)
	part, _ := writer.CreateFormFile("file", "task.txt")
	part.Write([]byte(text))
	writer.Close()

	resp, err := http.Post(srvBase+"/api/translate", writer.FormDataContentType(), body)
	if err != nil {
		t.Fatalf("translate POST failed: %v", err)
	}
	defer resp.Body.Close()
	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	if res["task_id"] == "" {
		t.Fatalf("no task id returned")
	}
	return res["task_id"]
}

func taskStatus(t *testing.T, srvBase, taskID string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(srvBase + "/api/task_status?task_id=" + taskID)
	if err != nil {
		t.Fatalf("task_status failed: %v", err)
	}
	defer resp.Body.Close()
	var st map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&st)
	return st
}

func waitTaskStatusOf(t *testing.T, srvBase, taskID, want string, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]interface{}
	for time.Now().Before(deadline) {
		last = taskStatus(t, srvBase, taskID)
		if s, _ := last["status"].(string); s == want {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach status %q within %v (last: %v)", taskID, want, timeout, last["status"])
	return nil
}

// TestPauseThenNewTaskThenResume reproduces two production deadlocks with the
// single default worker and NO SSE consumer attached (worst case for the
// message channel):
//  1. pause a running task, then start a second task — the worker must be
//     released and pick the new task up (previously it wedged forever on a
//     blocking log send into a full, unread channel);
//  2. resume the paused task afterwards — it must return to running and
//     complete (previously the resumed task was stranded: status clobbering
//     and a DoneCh double-close panic killed the worker).
func TestPauseThenNewTaskThenResume(t *testing.T) {
	// Model server: ~40ms per request so progress is observable.
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": "[T]译文内容"}},
			},
		})
	}))
	defer llm.Close()

	srv := startServer(t)
	defer srv.Close()

	// Task A: 150 paragraphs → after cancellation ~150 pause log lines,
	// far beyond the 100-slot channel buffer.
	textA := strings.TrimSuffix(strings.Repeat("Alpha paragraph to translate.\n\n", 150), "\n\n")
	taskA := submitTranslateTask(t, srv.BaseURL, llm.URL, textA)

	waitTaskStatusOf(t, srv.BaseURL, taskA, "running", 15*time.Second)
	time.Sleep(300 * time.Millisecond) // let a few paragraphs finish

	// Pause A. No SSE connection has ever been made for A.
	resp, err := http.Post(srv.BaseURL+"/api/pause?task_id="+taskA, "application/json", nil)
	if err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	resp.Body.Close()

	// The worker must wind A down to paused promptly (it used to deadlock
	// pushing pause messages into the full unread channel).
	waitTaskStatusOf(t, srv.BaseURL, taskA, "paused", 15*time.Second)

	// Bug 2: start task B while A is paused — B must actually run. B is a
	// single fast paragraph, so it may already be completed by the first
	// poll; the essential assertion is that it leaves "queued" at all
	// (a wedged worker would keep it queued forever).
	textB := "Beta paragraph for the second window."
	taskB := submitTranslateTask(t, srv.BaseURL, llm.URL, textB)
	waitTaskStatusOf(t, srv.BaseURL, taskB, "completed", 60*time.Second)

	// Bug 1: resume A — it must return to running and complete.
	resp2, err := http.Post(srv.BaseURL+"/api/resume?task_id="+taskA, "application/json", nil)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	resp2.Body.Close()
	waitTaskStatusOf(t, srv.BaseURL, taskA, "running", 15*time.Second)
	final := waitTaskStatusOf(t, srv.BaseURL, taskA, "completed", 120*time.Second)

	// The resumed run must have actually translated (the first pass covered
	// only a handful of the 150 paragraphs before the pause).
	stats, _ := final["stats"].(map[string]interface{})
	if success, _ := stats["success_count"].(float64); success < 100 {
		t.Errorf("resumed task translated too little: success_count=%v", success)
	}
}
