package middleware

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

type requestErrorsKey struct{}

type requestErrors struct {
	mu     sync.Mutex
	errors []error
}

func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	state, ok := ctx.Value(requestErrorsKey{}).(*requestErrors)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for i, previous := range state.errors {
		if errors.Is(previous, err) {
			return
		}
		if errors.Is(err, previous) {
			state.errors[i] = err
			return
		}
	}
	state.errors = append(state.errors, err)
}

func (s *requestErrors) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.errors...)
}

// ObserveRequests logs the start, completion, and internal errors of each request.
func ObserveRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := rand.Text()
		state := &requestErrors{}
		r = r.WithContext(context.WithValue(r.Context(), requestErrorsKey{}, state))
		response := &observedResponseWriter{ResponseWriter: w, ctx: r.Context()}
		w.Header().Set("X-Request-ID", requestID)
		requestLogger := logger.With("request_id", requestID, "method", r.Method, "url", r.URL.EscapedPath())
		requestLogger.InfoContext(r.Context(), "http.request.started")

		defer func() {
			var stack string
			aborted := false
			if value := recover(); value != nil {
				if value == http.ErrAbortHandler {
					RecordError(r.Context(), http.ErrAbortHandler)
					aborted = true
				} else {
					RecordError(r.Context(), fmt.Errorf("panic: %v", value))
					stack = string(debug.Stack())
					if response.status == 0 {
						http.Error(response, "Internal Server Error", http.StatusInternalServerError)
					} else {
						aborted = true
					}
				}
			}

			status := response.status
			if status == 0 && !aborted {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= 400 {
				level = slog.LevelWarn
			}
			attrs := []slog.Attr{
				slog.Int("status", status),
				slog.Float64("latency_ms", float64(time.Since(started))/float64(time.Millisecond)),
			}
			if err := state.err(); err != nil {
				level = slog.LevelError
				attrs = append(attrs, slog.String("error", err.Error()))
			}
			if status >= 500 || aborted {
				level = slog.LevelError
			}
			if stack != "" {
				attrs = append(attrs, slog.String("stack", stack))
			}
			if aborted {
				attrs = append(attrs, slog.Bool("aborted", true))
			}
			requestLogger.LogAttrs(r.Context(), level, "http.request.completed", attrs...)
			if aborted {
				// Let net/http close the connection without logging the panic twice.
				panic(http.ErrAbortHandler)
			}
		}()

		next.ServeHTTP(response, r)
	})
}

type observedResponseWriter struct {
	http.ResponseWriter
	ctx    context.Context
	status int
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *observedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.ResponseWriter.WriteHeader(status)
	// Informational responses (except protocol switching) are not final statuses.
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.status = status
	}
}

func (w *observedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	if err != nil {
		RecordError(w.ctx, err)
	}
	return n, err
}

// FlushError tracks the implicit status when used through http.ResponseController.
func (w *observedResponseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		RecordError(w.ctx, err)
	}
	return err
}
