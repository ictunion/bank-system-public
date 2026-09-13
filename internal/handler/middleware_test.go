package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodySize_AllowsBodyAtOrUnderLimit(t *testing.T) {
	const limit = 16

	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(strings.Repeat("a", limit)))
	MaxBodySize(limit, next).ServeHTTP(recorder, request)

	if readErr != nil {
		t.Errorf("read a %d-byte body against a %d-byte limit: %v", limit, limit, readErr)
	}
}

func TestMaxBodySize_RejectsBodyOverLimit(t *testing.T) {
	const limit = 16

	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(strings.Repeat("a", limit+1)))
	MaxBodySize(limit, next).ServeHTTP(recorder, request)

	if readErr == nil {
		t.Errorf("reading a body one byte over the limit succeeded, want an error from http.MaxBytesReader")
	}
}
