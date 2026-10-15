package database

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectionStringPreservesCredentials(t *testing.T) {
	t.Setenv("DB_HOST", "::1")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "user@name")
	t.Setenv("DB_PASSWORD", "p@ss:/?#% word")
	t.Setenv("DB_NAME", "barrack")
	t.Setenv("DB_SSLMODE", "disable")
	dsn, err := connectionString()
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("generated connection string could not be parsed")
	}
	c := config.ConnConfig
	if c.Host != "::1" || c.Port != 5433 || c.User != "user@name" || c.Password != "p@ss:/?#% word" || c.Database != "barrack" {
		t.Fatal("connection configuration did not preserve its values")
	}
}

func TestConnectionStringValidatesEnvironment(t *testing.T) {
	for _, key := range []string{"DB_USER", "DB_PASSWORD", "DB_NAME"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("DB_USER", "barrack")
			t.Setenv("DB_PASSWORD", "secret")
			t.Setenv("DB_NAME", "barrack")
			t.Setenv(key, "")
			if _, err := connectionString(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected validation error for %s", key)
			}
		})
	}
	for _, port := range []string{"abc", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("DB_USER", "barrack")
			t.Setenv("DB_PASSWORD", "secret")
			t.Setenv("DB_NAME", "barrack")
			t.Setenv("DB_PORT", port)
			if _, err := connectionString(); err == nil {
				t.Fatal("expected invalid port to be rejected")
			}
		})
	}
}
