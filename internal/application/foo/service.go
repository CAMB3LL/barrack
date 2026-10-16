// Package foo implements Foo use cases. Dependencies point toward the domain.
package foo

import (
	"context"

	"github.com/CAMB3LL/barrack/internal/domain/foo"
)

// Repository describes persistence required by the use cases, not a SQL API.
type Repository interface {
	Create(context.Context) (foo.Foo, error)
	Get(context.Context, int64) (foo.Foo, error)
	List(context.Context, int, int) ([]foo.Foo, error)
	Update(context.Context, int64) (foo.Foo, error)
	Delete(context.Context, int64) error
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) Create(ctx context.Context) (foo.Foo, error) { return s.repository.Create(ctx) }
func (s *Service) Get(ctx context.Context, id int64) (foo.Foo, error) {
	if id <= 0 {
		return foo.Foo{}, foo.ErrInvalidID
	}
	return s.repository.Get(ctx, id)
}
func (s *Service) List(ctx context.Context, limit, offset int) ([]foo.Foo, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, foo.ErrInvalidPage
	}
	return s.repository.List(ctx, limit, offset)
}

// Update touches the entity: Foo currently has no editable business attributes.
func (s *Service) Update(ctx context.Context, id int64) (foo.Foo, error) {
	if id <= 0 {
		return foo.Foo{}, foo.ErrInvalidID
	}
	return s.repository.Update(ctx, id)
}
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return foo.ErrInvalidID
	}
	return s.repository.Delete(ctx, id)
}
