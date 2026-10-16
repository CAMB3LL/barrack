package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestHealthTracksDatabaseAvailability(t *testing.T) {
	var databaseErr error
	logger, logs := newTestLogger()
	router := NewRouter(pingFunc(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("database probe must have a two-second deadline")
		}
		return databaseErr
	}), logger, nil)

	for _, tc := range []struct {
		name   string
		err    error
		code   int
		status string
		db     string
	}{
		{"healthy", nil, http.StatusOK, "ok", "up"},
		{"disconnected", errors.New("private connection details"), http.StatusServiceUnavailable, "unavailable", "down"},
		{"recovered", nil, http.StatusOK, "ok", "up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			databaseErr = tc.err
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
			if response.Code != tc.code {
				t.Fatalf("status = %d, want %d", response.Code, tc.code)
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["status"] != tc.status || body["database"] != tc.db {
				t.Fatalf("unexpected health response: %v", body)
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Error("health response must be JSON")
			}
			entries := readLogEntries(t, logs)
			assertRequestLogs(t, entries, response, http.MethodGet, "/health", tc.code)
			if tc.err != nil {
				if entries[1]["level"] != "ERROR" || entries[1]["error"] != "ping PostgreSQL: "+tc.err.Error() {
					t.Fatalf("database error missing from completion log: %v", entries[1])
				}
			} else if _, ok := entries[1]["error"]; ok {
				t.Fatal("healthy request inherited a previous error")
			}
		})
	}
}

func TestHealthHonorsRequestCancellation(t *testing.T) {
	logger, _ := newTestLogger()
	router := NewRouter(pingFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}), logger, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestRouterRejectsUnknownRoutesAndMethods(t *testing.T) {
	logger, logs := newTestLogger()
	router := NewRouter(pingFunc(func(context.Context) error {
		t.Fatal("unexpected database probe")
		return nil
	}), logger, nil)
	for _, tc := range []struct {
		method string
		path   string
		code   int
	}{
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/health", http.StatusMethodNotAllowed},
		{http.MethodGet, "/health/extra", http.StatusNotFound},
	} {
		logs.Reset()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.code {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, response.Code, tc.code)
		}
		entries := readLogEntries(t, logs)
		assertRequestLogs(t, entries, response, tc.method, tc.path, tc.code)
		if entries[1]["level"] != "WARN" {
			t.Errorf("unexpected level for HTTP %d: %v", tc.code, entries[1]["level"])
		}
	}
}

func TestHealthLogsEncodingFailure(t *testing.T) {
	logger, logs := newTestLogger()
	router := NewRouter(pingFunc(func(context.Context) error { return nil }), logger, nil)
	response := &failingResponseWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("connection reset")}
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	entries := readLogEntries(t, logs)
	if len(entries) != 2 || entries[1]["error"] != "encode health response: connection reset" || entries[1]["level"] != "ERROR" || entries[1]["status"] != float64(200) {
		t.Fatalf("unexpected encoding failure log: %v", entries)
	}
}
