package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRunRejectsInvalidConfigurationBeforeServing(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, tc := range []struct {
		name        string
		environment string
		port        string
		want        string
	}{
		{"environment", "", "8080", "APP_ENV"},
		{"HTTP port", "development", "invalid", "PORT"},
		{"database port", "development", "8080", "DB_PORT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.environment)
			t.Setenv("PORT", tc.port)
			t.Setenv("DB_USER", "test")
			t.Setenv("DB_PASSWORD", "test")
			t.Setenv("DB_NAME", "test")
			t.Setenv("DB_PORT", "invalid")
			if err := run(logger); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s error before HTTP startup, got %v", tc.want, err)
			}
		})
	}
}
