package local

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHealthRejectsIncompatibleAPI(t *testing.T) {
	client := &Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok","version":"2.0.0","api_version":"2"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	_, err := client.Health(context.Background())
	var incompatible *IncompatibleAPIError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Health error = %v, want IncompatibleAPIError", err)
	}
}
