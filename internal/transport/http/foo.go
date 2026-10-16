package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	application "github.com/CAMB3LL/barrack/internal/application/foo"
	"github.com/CAMB3LL/barrack/internal/domain/foo"
	"github.com/CAMB3LL/barrack/internal/transport/http/middleware"
)

type fooResponse struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func fooDTO(entity foo.Foo) fooResponse {
	return fooResponse{entity.ID, entity.CreatedAt, entity.UpdatedAt}
}

func registerFoo(mux *http.ServeMux, service *application.Service) {
	mux.HandleFunc("POST /foo", func(w http.ResponseWriter, r *http.Request) {
		if !emptyFooInput(w, r) {
			return
		}
		entity, err := service.Create(r.Context())
		if err != nil {
			writeFooError(w, r, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/foo/%d", entity.ID))
		writeJSON(w, r, http.StatusCreated, fooDTO(entity))
	})
	mux.HandleFunc("GET /foo", func(w http.ResponseWriter, r *http.Request) {
		limit, offset := 20, 0
		for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
			if r.URL.Query().Has(key) {
				value, err := strconv.Atoi(r.URL.Query().Get(key))
				if err != nil {
					writeFooError(w, r, foo.ErrInvalidPage)
					return
				}
				*target = value
			}
		}
		entities, err := service.List(r.Context(), limit, offset)
		if err != nil {
			writeFooError(w, r, err)
			return
		}
		response := make([]fooResponse, 0, len(entities))
		for _, entity := range entities {
			response = append(response, fooDTO(entity))
		}
		writeJSON(w, r, http.StatusOK, response)
	})
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		mux.HandleFunc(method+" /foo/{id}", func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil || id <= 0 {
				writeFooError(w, r, foo.ErrInvalidID)
				return
			}
			if r.Method == http.MethodDelete {
				if err := service.Delete(r.Context(), id); err != nil {
					writeFooError(w, r, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var entity foo.Foo
			if r.Method == http.MethodPut {
				if !emptyFooInput(w, r) {
					return
				}
				entity, err = service.Update(r.Context(), id)
			} else {
				entity, err = service.Get(r.Context(), id)
			}
			if err != nil {
				writeFooError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, fooDTO(entity))
		})
	}
}

// Foo has no writable fields. Accept an absent body or an empty JSON object.
func emptyFooInput(w http.ResponseWriter, r *http.Request) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	var fields map[string]json.RawMessage
	err := decoder.Decode(&fields)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err == nil && fields != nil && len(fields) == 0 {
		var extra any
		if errors.Is(decoder.Decode(&extra), io.EOF) {
			return true
		}
	}
	writeJSON(w, r, http.StatusBadRequest, map[string]string{"error": "body must be empty or an empty JSON object"})
	return false
}
func writeFooError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, foo.ErrNotFound):
		status, message = http.StatusNotFound, foo.ErrNotFound.Error()
	case errors.Is(err, foo.ErrInvalidID), errors.Is(err, foo.ErrInvalidPage):
		status, message = http.StatusBadRequest, err.Error()
	default:
		middleware.RecordError(r.Context(), err)
	}
	writeJSON(w, r, status, map[string]string{"error": message})
}
func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		middleware.RecordError(r.Context(), fmt.Errorf("encode response: %w", err))
	}
}
