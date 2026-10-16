package repositories

import (
	"context"
	"errors"
	"fmt"

	application "github.com/CAMB3LL/barrack/internal/application/foo"
	"github.com/CAMB3LL/barrack/internal/domain/foo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FooRepository struct{ pool *pgxpool.Pool }

var _ application.Repository = (*FooRepository)(nil)

func NewFooRepository(pool *pgxpool.Pool) *FooRepository { return &FooRepository{pool: pool} }

func scanFoo(row pgx.Row) (foo.Foo, error) {
	var entity foo.Foo
	err := row.Scan(&entity.ID, &entity.CreatedAt, &entity.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return foo.Foo{}, foo.ErrNotFound
	}
	if err != nil {
		return foo.Foo{}, fmt.Errorf("read foo: %w", err)
	}
	return entity, nil
}
func (r *FooRepository) Create(ctx context.Context) (foo.Foo, error) {
	return scanFoo(r.pool.QueryRow(ctx, "INSERT INTO public.foo DEFAULT VALUES RETURNING id, created_at, updated_at"))
}
func (r *FooRepository) Get(ctx context.Context, id int64) (foo.Foo, error) {
	return scanFoo(r.pool.QueryRow(ctx, "SELECT id, created_at, updated_at FROM public.foo WHERE id = $1", id))
}
func (r *FooRepository) List(ctx context.Context, limit, offset int) ([]foo.Foo, error) {
	rows, err := r.pool.Query(ctx, "SELECT id, created_at, updated_at FROM public.foo ORDER BY id LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list foo: %w", err)
	}
	defer rows.Close()
	entities := make([]foo.Foo, 0)
	for rows.Next() {
		entity, err := scanFoo(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foo: %w", err)
	}
	return entities, nil
}
func (r *FooRepository) Update(ctx context.Context, id int64) (foo.Foo, error) {
	// The existing trigger owns updated_at; created_at and identity stay immutable.
	return scanFoo(r.pool.QueryRow(ctx, "UPDATE public.foo SET updated_at = DEFAULT WHERE id = $1 RETURNING id, created_at, updated_at", id))
}
func (r *FooRepository) Delete(ctx context.Context, id int64) error {
	result, err := r.pool.Exec(ctx, "DELETE FROM public.foo WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete foo: %w", err)
	}
	if result.RowsAffected() == 0 {
		return foo.ErrNotFound
	}
	return nil
}
