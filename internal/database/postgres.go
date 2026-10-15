package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func connectionString() (string, error) {
	for _, key := range []string{"DB_USER", "DB_PASSWORD", "DB_NAME"} {
		if os.Getenv(key) == "" {
			return "", fmt.Errorf("%s is required", key)
		}
	}

	host := envOrDefault("DB_HOST", "localhost")
	port := envOrDefault("DB_PORT", "5432")
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("DB_PORT must be an integer between 1 and 65535")
	}

	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + os.Getenv("DB_NAME"),
	}
	query := url.Values{}
	query.Set("sslmode", envOrDefault("DB_SSLMODE", "require"))
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

func connectionConfig() (*pgxpool.Config, error) {
	dsn, err := connectionString()
	if err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Parsing errors can contain the connection string, including credentials.
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	return config, nil
}

// OpenSQL opens a short-lived administration pool using the same DB_* settings.
// Initialization uses one session for its lock and SQL transactions.
func OpenSQL(ctx context.Context) (*sql.DB, error) {
	config, err := connectionConfig()
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*config.ConnConfig)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL for initialization: %w", err)
	}
	return db, nil
}

// Open creates a pool and verifies connectivity before the HTTP server starts.
func Open(ctx context.Context) (*pgxpool.Pool, error) {
	config, err := connectionConfig()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
