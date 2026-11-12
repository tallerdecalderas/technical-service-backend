package observability

import (
	"bytes"
	"encoding/json"
	"testing"

	"log/slog"
)

func TestNewLoggerWritesStructuredMessagesAtConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(slog.LevelWarn, &output)

	logger.Info("hidden")
	logger.Warn("visible", "request_id", "request-123")

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("logger output is not valid JSON: %v", err)
	}
	if entry["msg"] != "visible" {
		t.Errorf("msg = %v, want visible", entry["msg"])
	}
	if entry["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entry["level"])
	}
	if entry["request_id"] != "request-123" {
		t.Errorf("request_id = %v, want request-123", entry["request_id"])
	}
}
