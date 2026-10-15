package management

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Integration tests create and drop uniquely named databases on an opt-in test
// PostgreSQL instance. The provided user must be allowed to CREATE DATABASE.
func isolatedDatabase(t *testing.T) (*sql.DB, *pgx.ConnConfig) {
	t.Helper()
	dsn := os.Getenv("BARRACK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BARRACK_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid BARRACK_TEST_DATABASE_URL")
	}
	config.ConnectTimeout = 5 * time.Second
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	name := "barrack_test_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{name}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	config.Database = name
	return testPool(t, config), config
}

func testPool(t *testing.T, config *pgx.ConnConfig) *sql.DB {
	t.Helper()
	db := stdlib.OpenDB(*config.Copy())
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func embeddedSQL(t *testing.T) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	if err := fs.WalkDir(files, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := fs.ReadFile(files, file)
			if err != nil {
				return err
			}
			result[file] = &fstest.MapFile{Data: data}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var value int
	if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestIntegrationStartupAndRestart(t *testing.T) {
	db, _ := isolatedDatabase(t)
	for _, environment := range []string{"development", "development", "test"} {
		if err := Initialize(t.Context(), db, environment, quietLogger()); err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM public.foo":                                                                                    5,
		"SELECT count(*) FROM schema_migrations WHERE is_applied":                                                            1,
		"SELECT count(*) FROM seed_history WHERE is_applied":                                                                 1,
		"SELECT count(*) FROM public.foo WHERE created_at IS NOT NULL AND updated_at IS NOT NULL":                            5,
		"SELECT count(*) FROM schema_migrations WHERE name = 'create_foo'":                                                   1,
		"SELECT count(*) FROM seed_history WHERE name = 'foo_initial_data'":                                                  1,
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'schema_migrations'": 4,
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'seed_history'":      4,
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('schema_migrations', 'seed_history') AND column_name IN ('version_id', 'filename', 'file', 'seed_group', 'group')": 0,
	} {
		if got := count(t, db, query); got != want {
			t.Fatalf("%s: got %d, want %d", query, got, want)
		}
	}
	var generated int64
	if err := db.QueryRowContext(t.Context(), "INSERT INTO public.foo DEFAULT VALUES RETURNING id").Scan(&generated); err != nil || generated <= 5 {
		t.Fatalf("identity did not advance: id=%d error=%v", generated, err)
	}
	if got := count(t, db, "WITH updated AS (UPDATE public.foo SET updated_at = '2000-01-01' WHERE id = 1 RETURNING created_at, updated_at) SELECT count(*) FROM updated WHERE updated_at > created_at"); got != 1 {
		t.Fatal("trigger did not replace updated_at and preserve created_at")
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO public.foo (created_at) VALUES (NULL)"); err == nil {
		t.Fatal("created_at accepted NULL")
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO public.foo (id) VALUES (42)"); err == nil {
		t.Fatal("GENERATED ALWAYS allowed a manually assigned identity")
	}
	// Removing seeded data does not cause the seed to run again.
	if _, err := db.ExecContext(t.Context(), "DELETE FROM public.foo WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(t.Context(), db, "development", quietLogger()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, "SELECT count(*) FROM public.foo WHERE id = 1"); got != 0 {
		t.Fatal("restart recreated a deleted fixture")
	}
}

func TestIntegrationMigrationsFollowIndexAndUseNames(t *testing.T) {
	db, _ := isolatedDatabase(t)
	source := embeddedSQL(t)
	columnFile := "migrations/add_foo_column.sql"
	source[columnFile] = &fstest.MapFile{Data: []byte("ALTER TABLE public.foo ADD COLUMN label TEXT;")}
	// The filename sorts before create_foo.sql, but the explicit index controls execution.
	index := append(append([]string(nil), migrationFiles...), columnFile)
	if err := initialize(t.Context(), db, "production", quietLogger(), source, index, seeds); err != nil {
		t.Fatal(err)
	}
	// Reordering already applied entries must not re-run either SQL file.
	index = []string{columnFile, migrationFiles[0]}
	if err := initialize(t.Context(), db, "production", quietLogger(), source, index, seeds); err != nil {
		t.Fatal(err)
	}
	indexFile := "migrations/add_foo_index.sql"
	source[indexFile] = &fstest.MapFile{Data: []byte("CREATE INDEX foo_label_idx ON public.foo (label);")}
	index = append(index, indexFile)
	if err := initialize(t.Context(), db, "production", quietLogger(), source, index, seeds); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM schema_migrations") != 3 || count(t, db, "SELECT count(*) FROM schema_migrations WHERE name = 'add_foo_index' AND is_applied") != 1 {
		t.Fatal("migration names did not identify applied and pending changes")
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO schema_migrations (name, is_applied) VALUES ('add_foo_index', TRUE)"); err == nil {
		t.Fatal("migration history accepted a duplicate name")
	}
}

func TestIntegrationProductionCatalogsAndDeferredFixture(t *testing.T) {
	db, _ := isolatedDatabase(t)
	source := embeddedSQL(t)
	source["migrations/create_catalog.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE public.catalog (code TEXT PRIMARY KEY, name TEXT NOT NULL);")}
	catalog := SeedDefinition{Name: "statuses", File: "seeds/catalog_status.sql", Group: SeedGroupCatalog}
	source[catalog.File] = &fstest.MapFile{Data: []byte("INSERT INTO public.catalog VALUES ('ACTIVE', 'Active') ON CONFLICT (code) DO NOTHING;")}
	index := append(append([]SeedDefinition(nil), seeds...), catalog)
	migrationIndex := append(append([]string(nil), migrationFiles...), "migrations/create_catalog.sql")
	for i := 0; i < 2; i++ {
		if err := initialize(t.Context(), db, "production", quietLogger(), source, migrationIndex, index); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 0 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'foo_initial_data'") != 0 {
		t.Fatal("production applied or marked the fixture")
	}
	if count(t, db, "SELECT count(*) FROM public.catalog WHERE code = 'ACTIVE'") != 1 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'statuses' AND is_applied") != 1 {
		t.Fatal("production catalog was not applied exactly once")
	}
	if count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'statuses'") != 1 || count(t, db, "SELECT count(*) FROM schema_migrations WHERE name = 'create_catalog'") != 1 {
		t.Fatal("migration and catalog names were not persisted")
	}
	// An excluded fixture must still be eligible in development after a catalog.
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationIndex, index); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 {
		t.Fatal("fixture was skipped because a catalog was applied first")
	}
	// A newly indexed catalog seed runs on the next startup.
	next := SeedDefinition{Name: "inactive_status", File: "seeds/catalog_inactive.sql", Group: SeedGroupCatalog}
	source[next.File] = &fstest.MapFile{Data: []byte("INSERT INTO public.catalog VALUES ('INACTIVE', 'Inactive') ON CONFLICT (code) DO NOTHING;")}
	index = append(index, next)
	if err := initialize(t.Context(), db, "production", quietLogger(), source, migrationIndex, index); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM public.catalog") != 2 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'inactive_status' AND is_applied") != 1 {
		t.Fatal("new catalog seed was not applied")
	}
}

func TestIntegrationSeedFailureRollbackAndRetry(t *testing.T) {
	db, _ := isolatedDatabase(t)
	source := embeddedSQL(t)
	failing := SeedDefinition{Name: "failing", File: "seeds/catalog_failure.sql", Group: SeedGroupCatalog}
	index := append(append([]SeedDefinition(nil), seeds...), failing)
	source[failing.File] = &fstest.MapFile{Data: []byte("INSERT INTO public.foo DEFAULT VALUES;\nSELECT 1 / 0;")}
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationFiles, index); err == nil {
		t.Fatal("expected seed failure")
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'failing'") != 0 {
		t.Fatal("failed seed left partial data or a history entry")
	}
	// Fixing an unapplied file is allowed; applied files remain immutable.
	source[failing.File] = &fstest.MapFile{Data: []byte("INSERT INTO public.foo DEFAULT VALUES;")}
	for i := 0; i < 2; i++ {
		if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationFiles, index); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 6 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'failing' AND is_applied") != 1 {
		t.Fatal("seed retry did not complete exactly once")
	}
}

func TestIntegrationHistoryFailureRollsBackEntireSeed(t *testing.T) {
	db, _ := isolatedDatabase(t)
	// Production creates foo without applying its development fixture.
	if err := Initialize(t.Context(), db, "production", quietLogger()); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(t.Context(), `
		CREATE FUNCTION public.reject_seed_record() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'simulated history insertion failure';
		END;
		$$;
		CREATE TRIGGER reject_seed_record BEFORE INSERT ON public.seed_history
		FOR EACH ROW EXECUTE FUNCTION public.reject_seed_record();
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(t.Context(), db, "development", quietLogger()); err == nil || !strings.Contains(err.Error(), "simulated history insertion failure") {
		t.Fatalf("expected history insertion failure after seed execution, got %v", err)
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 0 || count(t, db, "SELECT count(*) FROM public.seed_history WHERE name = 'foo_initial_data'") != 0 {
		t.Fatal("a failed history insertion left seed rows or marked the seed as applied")
	}
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER reject_seed_record ON public.seed_history; DROP FUNCTION public.reject_seed_record();"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Initialize(t.Context(), db, "development", quietLogger()); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'foo_initial_data' AND is_applied") != 1 {
		t.Fatal("retry did not apply the complete seed and its name exactly once")
	}
}

func TestIntegrationInitialMigrationFailureLeavesNoHistory(t *testing.T) {
	db, _ := isolatedDatabase(t)
	source := embeddedSQL(t)
	file := "migrations/create_foo.sql"
	original := source[file].Data
	source[file] = &fstest.MapFile{Data: append(append([]byte(nil), original...), []byte("\nSELECT 1 / 0;")...)}
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationFiles, seeds); err == nil {
		t.Fatal("expected initial migration failure")
	}
	if count(t, db, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('schema_migrations', 'seed_history', 'foo')") != 0 {
		t.Fatal("failed initial migration left tables or synthetic history records")
	}
	source[file] = &fstest.MapFile{Data: original}
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationFiles, seeds); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 || count(t, db, "SELECT count(*) FROM schema_migrations WHERE name = 'create_foo'") != 1 {
		t.Fatal("retry did not create the schema and apply the seed")
	}
}

func TestIntegrationMigrationFailureRollbackAndRetry(t *testing.T) {
	db, _ := isolatedDatabase(t)
	source := embeddedSQL(t)
	file := "migrations/failure.sql"
	migrationIndex := append(append([]string(nil), migrationFiles...), file)
	source[file] = &fstest.MapFile{Data: []byte("CREATE TABLE public.partial (id BIGINT);\nSELECT 1 / 0;")}
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationIndex, seeds); err == nil {
		t.Fatal("expected migration failure")
	}
	if count(t, db, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'partial'") != 0 || count(t, db, "SELECT count(*) FROM schema_migrations WHERE name = 'failure'") != 0 || count(t, db, "SELECT count(*) FROM public.foo") != 0 {
		t.Fatal("failed migration left partial changes or allowed seeds to run")
	}
	source[file] = &fstest.MapFile{Data: []byte("CREATE TABLE public.partial (id BIGINT);")}
	if err := initialize(t.Context(), db, "development", quietLogger(), source, migrationIndex, seeds); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 {
		t.Fatal("seeds did not run after migration retry")
	}
}

func TestIntegrationConcurrentStartups(t *testing.T) {
	db, config := isolatedDatabase(t)
	const instances = 6
	pools := make([]*sql.DB, instances)
	for i := range pools {
		pools[i] = testPool(t, config)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	results := make(chan error, instances)
	var workers sync.WaitGroup
	for _, pool := range pools {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- Initialize(ctx, pool, "development", quietLogger())
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'foo_initial_data' AND is_applied") != 1 {
		t.Fatal("concurrent startups duplicated seed data or history")
	}
}

func TestIntegrationCancellationReleasesLockAndRollsBack(t *testing.T) {
	db, _ := isolatedDatabase(t)
	if err := Initialize(t.Context(), db, "development", quietLogger()); err != nil {
		t.Fatal(err)
	}
	source := embeddedSQL(t)
	seed := SeedDefinition{Name: "slow", File: "seeds/catalog_slow.sql", Group: SeedGroupCatalog}
	source[seed.File] = &fstest.MapFile{Data: []byte("INSERT INTO public.foo DEFAULT VALUES;\nSELECT pg_sleep(10);")}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if err := initialize(ctx, db, "development", quietLogger(), source, migrationFiles, append(append([]SeedDefinition(nil), seeds...), seed)); err == nil {
		t.Fatal("expected canceled initialization")
	}
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if err := Initialize(ctx, db, "development", quietLogger()); err != nil {
		t.Fatalf("lock was not released after cancellation: %v", err)
	}
	if count(t, db, "SELECT count(*) FROM public.foo") != 5 || count(t, db, "SELECT count(*) FROM seed_history WHERE name = 'slow'") != 0 {
		t.Fatal("canceled seed left partial changes")
	}
}
