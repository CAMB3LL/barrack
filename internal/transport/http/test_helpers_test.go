package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	output := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(output, nil)), output
}

func readLogEntries(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("invalid JSON log: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func assertRequestLogs(t *testing.T, entries []map[string]any, response *httptest.ResponseRecorder, method, path string, status int) {
	t.Helper()
	if len(entries) != 2 {
		t.Fatalf("got %d events, want 2: %v", len(entries), entries)
	}
	if entries[0]["msg"] != "http.request.started" || entries[1]["msg"] != "http.request.completed" {
		t.Fatalf("unexpected events: %v", entries)
	}
	id := response.Header().Get("X-Request-ID")
	if id == "" || entries[0]["request_id"] != id || entries[1]["request_id"] != id {
		t.Fatal("request ID missing or not correlated")
	}
	for _, entry := range entries {
		if entry["method"] != method || entry["url"] != path || entry["time"] == nil {
			t.Fatalf("unexpected request attributes: %v", entry)
		}
	}
	if entries[0]["level"] != "INFO" || entries[1]["status"] != float64(status) {
		t.Fatalf("unexpected request level or status: %v", entries)
	}
	latency, ok := entries[1]["latency_ms"].(float64)
	if !ok || latency < 0 {
		t.Fatalf("invalid latency: %v", entries[1]["latency_ms"])
	}
}

type failingResponseWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, w.err }
