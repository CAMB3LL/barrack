package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	application "github.com/CAMB3LL/barrack/internal/application/foo"
	"github.com/CAMB3LL/barrack/internal/domain/foo"
)

type fooStub struct {
	calls         int
	err           error
	limit, offset int
}

func (s *fooStub) Create(context.Context) (foo.Foo, error) { s.calls++; return foo.Foo{ID: 1}, s.err }
func (s *fooStub) Get(context.Context, int64) (foo.Foo, error) {
	s.calls++
	return foo.Foo{ID: 1}, s.err
}
func (s *fooStub) Update(context.Context, int64) (foo.Foo, error) {
	s.calls++
	return foo.Foo{ID: 1}, s.err
}
func (s *fooStub) Delete(context.Context, int64) error { s.calls++; return s.err }
func (s *fooStub) List(_ context.Context, limit, offset int) ([]foo.Foo, error) {
	s.calls++
	s.limit, s.offset = limit, offset
	return []foo.Foo{}, s.err
}

func TestFooHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		err                error
		status, calls      int
	}{
		{"POST", "/foo", "", nil, 201, 1},
		{"POST", "/foo", "{}", nil, 201, 1},
		{"GET", "/foo/1", "", nil, 200, 1},
		{"PUT", "/foo/1", "{}", nil, 200, 1},
		{"DELETE", "/foo/1", "", nil, 204, 1},
		{"GET", "/foo?limit=5&offset=2", "", nil, 200, 1},
		{"GET", "/foo/0", "", nil, 400, 0},
		{"GET", "/foo/9223372036854775808", "", nil, 400, 0},
		{"GET", "/foo/abc", "", nil, 400, 0},
		{"GET", "/foo?limit=101", "", nil, 400, 0},
		{"GET", "/foo?offset=-1", "", nil, 400, 0},
		{"GET", "/foo?limit=x", "", nil, 400, 0},
		{"POST", "/foo", `{"id":1}`, nil, 400, 0},
		{"PUT", "/foo/1", `{"updated_at":"2000-01-01"}`, nil, 400, 0},
		{"POST", "/foo", `{} {}`, nil, 400, 0},
		{"POST", "/foo", `null`, nil, 400, 0},
		{"POST", "/foo", `{`, nil, 400, 0},
		{"POST", "/foo", strings.Repeat(" ", 1025), nil, 400, 0},
		{"GET", "/foo/1", "", foo.ErrNotFound, 404, 1},
		{"PUT", "/foo/1", "", foo.ErrNotFound, 404, 1},
		{"DELETE", "/foo/1", "", foo.ErrNotFound, 404, 1},
		{"GET", "/foo/1", "", errors.New("private database details"), 500, 1},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			repo := &fooStub{err: tc.err}
			logger, logs := newTestLogger()
			router := NewRouter(pingFunc(func(context.Context) error { return nil }), logger, application.NewService(repo))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status || repo.calls != tc.calls {
				t.Fatalf("status=%d calls=%d; want %d, %d: %s", response.Code, repo.calls, tc.status, tc.calls, response.Body)
			}
			if tc.status == 204 {
				if response.Body.Len() != 0 {
					t.Fatal("delete returned a body")
				}
			} else if !json.Valid(response.Body.Bytes()) {
				t.Fatal("response is not JSON")
			}
			if tc.status == 201 && response.Header().Get("Location") != "/foo/1" {
				t.Fatal("missing resource location")
			}
			if tc.path == "/foo?limit=5&offset=2" && (repo.limit != 5 || repo.offset != 2 || response.Body.String() != "[]\n") {
				t.Fatal("incorrect pagination or empty list")
			}
			if tc.status == 500 {
				if strings.Contains(response.Body.String(), "private") || !strings.Contains(logs.String(), "private database details") {
					t.Fatal("internal error must only appear in logs")
				}
			}
		})
	}
}
