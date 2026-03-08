package test

import (
	"encoding/json"
	"testing"

	"auto_translate/pkg/webtask"
)

func TestSSEPayloadContainsETAFields(t *testing.T) {
	msg := webtask.LogMsg{
		Type:       "gray",
		Message:    "progress",
		Total:      10,
		Current:    4,
		Status:     "running",
		ElapsedSec: 32,
		EtaSec:     48,
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, ok := payload["elapsed_sec"]; !ok {
		t.Fatalf("expected elapsed_sec field in SSE payload")
	}
	if _, ok := payload["eta_sec"]; !ok {
		t.Fatalf("expected eta_sec field in SSE payload")
	}
}

func TestSSEPayloadCompletedETAZero(t *testing.T) {
	msg := webtask.LogMsg{
		Status:     "completed",
		Total:      8,
		Current:    8,
		ElapsedSec: 100,
		EtaSec:     0,
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if payload["status"] != "completed" {
		t.Fatalf("expected status completed, got %v", payload["status"])
	}
	if payload["eta_sec"].(float64) != 0 {
		t.Fatalf("expected completed eta_sec = 0, got %v", payload["eta_sec"])
	}
}
