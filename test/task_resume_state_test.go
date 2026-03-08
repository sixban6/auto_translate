package test

import (
	"auto_translate/pkg/config"
	"auto_translate/pkg/processor"
	"encoding/json"
	"testing"
)

type TaskState struct {
	ID              string                     `json:"id"`
	Total           int                        `json:"total"`
	Current         int                        `json:"current"`
	Status          string                     `json:"status"`
	InputPath       string                     `json:"input_path"`
	OutPath         string                     `json:"out_path"`
	Config          *config.Config             `json:"config"`
	CompletedChunks map[string]string          `json:"completed_chunks"`
	Stats           processor.TranslationStats `json:"stats"`
}

func TestTaskResumeStateSerialization(t *testing.T) {
	state := TaskState{
		ID:        "task_123",
		Total:     100,
		Current:   50,
		Status:    "disconnected",
		InputPath: "temp_uploads/task_123.txt",
		OutPath:   "temp_uploads/task_123_translated.txt",
		CompletedChunks: map[string]string{
			"chunk-1": "translated1",
		},
		Stats: processor.TranslationStats{
			SuccessCount: 1,
		},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed TaskState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.ID != "task_123" {
		t.Errorf("Expected ID task_123, got %s", parsed.ID)
	}
	if parsed.Current != 50 {
		t.Errorf("Expected Current 50, got %d", parsed.Current)
	}
	if parsed.CompletedChunks["chunk-1"] != "translated1" {
		t.Errorf("Expected chunk-1 to be translated1, got %v", parsed.CompletedChunks["chunk-1"])
	}
	if parsed.Stats.SuccessCount != 1 {
		t.Errorf("Expected Stats.SuccessCount 1, got %d", parsed.Stats.SuccessCount)
	}
}
