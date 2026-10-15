package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestObservedStatuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  int
		level   string
	}{
		{"empty", func(http.ResponseWriter, *http.Request) {}, 200, "INFO"},
		{"implicit", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }, 200, "INFO"},
		{"explicit", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) }, 201, "INFO"},
		{"client error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }, 400, "WARN"},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, 503, "ERROR"},
		{"first header wins", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202); w.WriteHeader(500) }, 202, "INFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := newTestLogger()
			response := httptest.NewRecorder()
			ObserveRequests(logger, tc.handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
			entries := readLogEntries(t, logs)
			assertRequestLogs(t, entries, response, http.MethodGet, "/test", tc.status)
			if response.Code != tc.status || entries[1]["level"] != tc.level {
				t.Fatalf("unexpected response or level: %d %v", response.Code, entries[1])
			}
			if _, ok := entries[1]["error"]; ok {
				t.Error("middleware invented an internal error")
			}
		})
	}
}

func TestRequestLogsOmitSensitiveRequestData(t *testing.T) {
	logger, logs := newTestLogger()
	handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response-body-secret"))
	}))
	request := httptest.NewRequest(http.MethodPost, "http://user:password@localhost/test%20path?token=query-secret", strings.NewReader("request-body-secret"))
	request.Header.Set("Authorization", "Bearer header-secret")
	request.Header.Set("X-Request-ID", "untrusted-request-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertRequestLogs(t, readLogEntries(t, logs), response, http.MethodPost, "/test%20path", 200)
	for _, secret := range []string{"password", "query-secret", "header-secret", "request-body-secret", "response-body-secret", "untrusted-request-id"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

func TestRecordErrorAccumulatesRequestErrors(t *testing.T) {
	RecordError(context.Background(), errors.New("outside request"))
	logger, logs := newTestLogger()
	handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := errors.New("first failure")
		RecordError(r.Context(), nil)
		RecordError(r.Context(), first)
		RecordError(r.Context(), fmt.Errorf("handler: %w", first))
		RecordError(r.Context(), errors.New("second failure"))
		w.WriteHeader(http.StatusAccepted)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
	entries := readLogEntries(t, logs)
	assertRequestLogs(t, entries, response, http.MethodGet, "/test", 202)
	if entries[1]["error"] != "handler: first failure\nsecond failure" || entries[1]["level"] != "ERROR" {
		t.Fatalf("errors were not accumulated: %v", entries[1])
	}
}

func TestObservedPanics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		value  any
		want   int
		abort  bool
	}{
		{"before response", 0, "private panic details", 500, false},
		{"after response", 202, "private panic details", 202, true},
		{"abort without response", 0, http.ErrAbortHandler, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := newTestLogger()
			handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("already sent"))
				}
				panic(tc.value)
			}))
			response := httptest.NewRecorder()
			var caught any
			func() {
				defer func() { caught = recover() }()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
			}()
			if tc.abort && caught != http.ErrAbortHandler || !tc.abort && caught != nil {
				t.Fatalf("unexpected propagated panic: %v", caught)
			}
			entries := readLogEntries(t, logs)
			assertRequestLogs(t, entries, response, http.MethodGet, "/test", tc.want)
			if entries[1]["level"] != "ERROR" || entries[1]["error"] == nil {
				t.Fatalf("panic not recorded: %v", entries[1])
			}
			if tc.value != http.ErrAbortHandler && !strings.Contains(fmt.Sprint(entries[1]["stack"]), "TestObservedPanics") {
				t.Fatal("panic stack missing")
			}
			if tc.abort && entries[1]["aborted"] != true {
				t.Fatal("aborted response not marked")
			}
			if strings.Contains(response.Body.String(), "private panic details") {
				t.Fatal("panic details leaked to client")
			}
			if tc.status != 0 && response.Body.String() != "already sent" {
				t.Fatal("middleware modified the committed response")
			}
			if !tc.abort && (response.Code != 500 || response.Body.String() != "Internal Server Error\n") {
				t.Fatal("expected generic 500 response")
			}
		})
	}
}

type failingResponseWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, w.err }

func TestObservedWriteFailure(t *testing.T) {
	logger, logs := newTestLogger()
	response := &failingResponseWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("broken pipe")}
	var writeErr error
	handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, writeErr = w.Write([]byte("ok"))
	}))
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
	entries := readLogEntries(t, logs)
	if !errors.Is(writeErr, response.err) || len(entries) != 2 || entries[1]["error"] != "broken pipe" || entries[1]["level"] != "ERROR" || entries[1]["status"] != float64(200) {
		t.Fatalf("write failure not preserved and recorded: %v", entries)
	}
}

func TestObservedResponseController(t *testing.T) {
	logger, logs := newTestLogger()
	response := httptest.NewRecorder()
	handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok || unwrapper.Unwrap() != response {
			t.Fatal("underlying writer not available")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
	assertRequestLogs(t, readLogEntries(t, logs), response, http.MethodGet, "/test", 200)
	if !response.Flushed || response.Code != 200 {
		t.Fatal("flush did not preserve implicit 200")
	}
}

func TestInformationalStatusDoesNotReplaceFinalStatus(t *testing.T) {
	logger, logs := newTestLogger()
	server := httptest.NewServer(ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Get(server.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	// Wait for handlers to finish before inspecting the shared log buffer.
	server.Close()
	entries := readLogEntries(t, logs)
	if response.StatusCode != 201 || len(entries) != 2 || entries[1]["status"] != float64(201) {
		t.Fatalf("informational response replaced final status: %v", entries)
	}
}

func TestPanicAfterFlushAbortsConnection(t *testing.T) {
	logger, logs := newTestLogger()
	server := httptest.NewUnstartedServer(ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial response"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			panic(err)
		}
		panic("handler failed after flush")
	})))
	server.Config.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Get(server.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || response.StatusCode != 202 || string(body) != "partial response" {
		t.Fatalf("expected interrupted 202 response, got status=%d body=%q error=%v", response.StatusCode, body, err)
	}
	server.Close()
	entries := readLogEntries(t, logs)
	if len(entries) != 2 || entries[1]["status"] != float64(202) || entries[1]["aborted"] != true || entries[1]["level"] != "ERROR" {
		t.Fatalf("unexpected abort logs: %v", entries)
	}
}

func TestConcurrentRequestsHaveIndependentLogs(t *testing.T) {
	logger, logs := newTestLogger()
	handler := ObserveRequests(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var workers sync.WaitGroup
		for i := 0; i < 3; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				RecordError(r.Context(), errors.New(r.URL.Path))
			}()
		}
		workers.Wait()
		w.WriteHeader(503)
	}))
	const count = 32
	responses := make([]*httptest.ResponseRecorder, count)
	var requests sync.WaitGroup
	for i := 0; i < count; i++ {
		requests.Add(1)
		go func(i int) {
			defer requests.Done()
			responses[i] = httptest.NewRecorder()
			handler.ServeHTTP(responses[i], httptest.NewRequest(http.MethodGet, fmt.Sprintf("/request/%d", i), nil))
		}(i)
	}
	requests.Wait()
	grouped := make(map[string][]map[string]any)
	for _, entry := range readLogEntries(t, logs) {
		id, ok := entry["request_id"].(string)
		if !ok {
			t.Fatal("request ID missing")
		}
		grouped[id] = append(grouped[id], entry)
	}
	if len(grouped) != count {
		t.Fatalf("got %d unique request IDs, want %d", len(grouped), count)
	}
	for i, response := range responses {
		entries := grouped[response.Header().Get("X-Request-ID")]
		path := fmt.Sprintf("/request/%d", i)
		assertRequestLogs(t, entries, response, http.MethodGet, path, 503)
		if entries[1]["error"] != strings.Join([]string{path, path, path}, "\n") {
			t.Fatalf("request inherited another request's error: %v", entries[1])
		}
	}
}
