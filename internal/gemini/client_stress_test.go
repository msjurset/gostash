package gemini

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestClientSmartFallbackBreakout verifies that permanent errors (400, 401, 403)
// and budget-exceeded 429 errors immediately abort the model fallback and retry loops.
func TestClientSmartFallbackBreakout(t *testing.T) {
	setTestConfig(t, []string{"fallback-1", "fallback-2"})

	cases := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"400 Bad Request", http.StatusBadRequest, "bad request"},
		{"401 Unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"403 Forbidden", http.StatusForbidden, "forbidden"},
		{"429 Budget Exceeded", http.StatusTooManyRequests, "budget-exceeded"},
		{"429 Quota Exceeded", http.StatusTooManyRequests, "quota exceeded"},
		{"429 Billing Not Enabled", http.StatusTooManyRequests, "billing not enabled"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := New()
			client.Model = "primary-model"

			var attempts int
			client.HTTP = &http.Client{
				Transport: &mockTransport{
					roundTrip: func(req *http.Request) (*http.Response, error) {
						attempts++
						return &http.Response{
							StatusCode: tc.statusCode,
							Body:       io.NopCloser(bytes.NewBufferString(tc.body)),
							Header:     make(http.Header),
						}, nil
					},
				},
			}

			ctx := context.Background()
			media := []Media{{Data: []byte("fake"), MimeType: "image/jpeg"}}
			_, err := client.Identify(ctx, "fake-api-key", media, "test prompt")
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			// Must fail on the first attempt of the first model. No retries or fallback models.
			if attempts != 1 {
				t.Errorf("Expected exactly 1 HTTP call, got %d", attempts)
			}
		})
	}
}

// TestClientEagerContextCancellation verifies that context cancellation halts retry loops
// and model iterations immediately without starting new requests.
func TestClientEagerContextCancellation(t *testing.T) {
	setTestConfig(t, []string{"fallback-1", "fallback-2"})

	client := New()
	client.Model = "primary-model"

	ctx, cancel := context.WithCancel(context.Background())

	var attempts int
	client.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					// Cancel the context during the first request
					cancel()
					// Return transient error so it would normally retry or fallback
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       io.NopCloser(bytes.NewBufferString("Service Unavailable")),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("should not reach here")),
					Header:     make(http.Header),
				}, nil
			},
		},
	}

	media := []Media{{Data: []byte("fake"), MimeType: "image/jpeg"}}
	_, err := client.Identify(ctx, "fake-api-key", media, "test prompt")
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && err != nil && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected context canceled, got error: %v", err)
	}

	// Must stop after the first request fails and cancellation is detected.
	// No retries on primary-model, no fallback-1 or fallback-2 calls.
	if attempts != 1 {
		t.Errorf("Expected exactly 1 attempt, got %d", attempts)
	}
}
