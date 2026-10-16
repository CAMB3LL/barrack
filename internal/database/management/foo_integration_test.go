package management

import (
	"errors"
	"testing"

	application "github.com/CAMB3LL/barrack/internal/application/foo"
	"github.com/CAMB3LL/barrack/internal/database/repositories"
	"github.com/CAMB3LL/barrack/internal/domain/foo"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationFooCRUD(t *testing.T) {
	db, config := isolatedDatabase(t)
	if err := Initialize(t.Context(), db, "production", quietLogger()); err != nil {
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig = config.Copy()
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	service := application.NewService(repositories.NewFooRepository(pool))
	first, err := service.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID <= 0 || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("invalid generated entity: %+v", first)
	}
	got, err := service.Get(t.Context(), first.ID)
	if err != nil || got != first {
		t.Fatalf("get: %+v, %v", got, err)
	}
	second, err := service.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(t.Context(), 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != second.ID {
		t.Fatalf("pagination: %+v, %v", page, err)
	}
	updated, err := service.Update(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) || !updated.UpdatedAt.After(first.UpdatedAt) {
		t.Fatal("update did not preserve creation time and advance update time")
	}
	if err := service.Delete(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(t.Context(), first.ID); !errors.Is(err, foo.ErrNotFound) {
		t.Fatalf("get deleted entity: %v", err)
	}
	if _, err := service.Update(t.Context(), first.ID); !errors.Is(err, foo.ErrNotFound) {
		t.Fatalf("update deleted entity: %v", err)
	}
	if err := service.Delete(t.Context(), first.ID); !errors.Is(err, foo.ErrNotFound) {
		t.Fatalf("delete missing entity: %v", err)
	}
}
