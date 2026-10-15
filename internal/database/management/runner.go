package management

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"
)

//go:embed migrations seeds
var files embed.FS

const initializationLockID int64 = 0x6261727261636b

type change struct {
	name string
	file string
}

// Files run in declaration order. Applied migrations are identified by their
// filename without .sql; keep that name unchanged after deployment.
var migrationFiles = []string{
	"migrations/create_foo.sql",
}

// Initialize applies SQL migrations and eligible seeds before HTTP starts.
func Initialize(ctx context.Context, db *sql.DB, environment string, logger *slog.Logger) error {
	return initialize(ctx, db, environment, logger, files, migrationFiles, seeds)
}

func initialize(ctx context.Context, db *sql.DB, environment string, logger *slog.Logger, source fs.FS, migrationIndex []string, index []SeedDefinition) (retErr error) {
	ordered, err := validateIndex(environment, source, index)
	if err != nil {
		return err
	}
	migrations, err := loadMigrations(source, migrationIndex)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	started := time.Now()
	logger.InfoContext(ctx, "database.initialization.started", "environment", environment)
	defer func() {
		if retErr != nil {
			logger.ErrorContext(ctx, "database.initialization.failed", "error", retErr)
		} else {
			logger.InfoContext(ctx, "database.initialization.completed", "duration_ms", time.Since(started).Seconds()*1000)
		}
	}()

	// One session holds the lock and runs all transactions. Other instances wait
	// here before checking histories or applying changes.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	lockCtx, cancelLock := context.WithTimeout(ctx, time.Minute)
	_, err = conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", initializationLockID)
	cancelLock()
	if err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return fmt.Errorf("acquire initialization lock: %w", err)
	}
	defer func() {
		unlockCtx, cancelUnlock := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelUnlock()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", initializationLockID); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, fmt.Errorf("release initialization lock: %w", err))
		}
	}()

	for _, migration := range migrations {
		started := time.Now()
		applied, err := applyChange(ctx, conn, source, "schema_migrations", migration)
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if applied {
			logger.InfoContext(ctx, "database.migration.applied", "name", migration.name,
				"duration_ms", time.Since(started).Seconds()*1000)
		}
	}
	for _, seed := range ordered {
		if seed.Group == SeedGroupFixture && environment == "production" {
			logger.InfoContext(ctx, "database.seed.excluded", "name", seed.Name)
			continue
		}
		started := time.Now()
		applied, err := applyChange(ctx, conn, source, "seed_history", change{seed.Name, seed.File})
		if err != nil {
			return fmt.Errorf("apply seed %s: %w", seed.Name, err)
		}
		event := "database.seed.skipped"
		if applied {
			event = "database.seed.applied"
		}
		logger.InfoContext(ctx, event, "name", seed.Name,
			"duration_ms", time.Since(started).Seconds()*1000)
	}
	return nil
}

// applyChange owns the transaction: SQL and history either both commit or both
// roll back. SQL files must not contain transaction control commands.
func applyChange(ctx context.Context, conn *sql.Conn, source fs.FS, table string, item change) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// The first migration creates both histories in its own transaction.
	var historyExists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&historyExists); err != nil {
		return false, err
	}
	if historyExists {
		var name string
		err := tx.QueryRowContext(ctx, "SELECT name FROM public."+table+" WHERE name = $1 AND is_applied", item.name).Scan(&name)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	contents, err := fs.ReadFile(source, item.file)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO public."+table+" (name, is_applied) VALUES ($1, TRUE)", item.name); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func loadMigrations(source fs.FS, index []string) ([]change, error) {
	var migrations []change
	registered := make(map[string]bool)
	for _, file := range index {
		name := strings.TrimSuffix(path.Base(file), ".sql")
		if !fs.ValidPath(file) || path.Dir(file) != "migrations" || path.Ext(file) != ".sql" || name == "" || registered[file] {
			return nil, fmt.Errorf("invalid or duplicate migration file: %s", file)
		}
		if info, err := fs.Stat(source, file); err != nil || info.IsDir() {
			return nil, fmt.Errorf("migration file not found: %s", file)
		}
		registered[file] = true
		migrations = append(migrations, change{name, file})
	}
	if err := fs.WalkDir(source, "migrations", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path.Ext(file) == ".sql" && !registered[file] {
			return fmt.Errorf("migration SQL file is not registered: %s", file)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(migrations) == 0 {
		return nil, errors.New("no SQL migrations found")
	}
	return migrations, nil
}
