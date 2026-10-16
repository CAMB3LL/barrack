package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	application "github.com/CAMB3LL/barrack/internal/application/foo"
	"github.com/CAMB3LL/barrack/internal/transport/http/middleware"
)

type Pinger interface {
	Ping(context.Context) error
}

func NewRouter(db Pinger, logger *slog.Logger, fooService *application.Service) http.Handler {
	mux := http.NewServeMux()
	if fooService != nil {
		registerFoo(mux, fooService)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		statusCode := http.StatusOK
		status, databaseStatus := "ok", "up"
		if err := db.Ping(ctx); err != nil {
			middleware.RecordError(r.Context(), fmt.Errorf("ping PostgreSQL: %w", err))
			statusCode = http.StatusServiceUnavailable
			status, databaseStatus = "unavailable", "down"
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusCode)
		if err := json.NewEncoder(w).Encode(struct {
			Status   string `json:"status"`
			Database string `json:"database"`
		}{Status: status, Database: databaseStatus}); err != nil {
			middleware.RecordError(r.Context(), fmt.Errorf("encode health response: %w", err))
		}
	})
	return middleware.ObserveRequests(logger, mux)
}
