// Package foo defines the Foo entity independently of persistence and transport.
package foo

import (
	"errors"
	"time"
)

var (
	ErrNotFound    = errors.New("foo not found")
	ErrInvalidID   = errors.New("foo id must be positive")
	ErrInvalidPage = errors.New("limit must be between 1 and 100 and offset must be non-negative")
)

type Foo struct {
	ID        int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
