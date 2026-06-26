package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/config"
)

func init() {
	backoffMin = 1 * time.Millisecond
}


type mockTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func setTestConfig(t *testing.T, models []string) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		config.Reload()
	})

	cfgDir := filepath.Join(tempDir, ".config", "stash")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "ai_models = ["
	for i, m := range models {
		if i > 0 {
			content += ", "
		}
		content += fmt.Sprintf("%q", m)
	}
	content += "]\n"

	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	config.Reload()
}

func TestClientFallbackChain(t *testing.T) {
	setTestConfig(t, []string{"fallback-model-1", "fallback-model-2"})

	client := New()
	client.Model = "primary-model"

	var calls []string
	client.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				// URL path contains the model name: /v1/models/{model}:generateContent
				path := req.URL.Path
				modelName := ""
				if strings.Contains(path, "/models/") {
					parts := strings.Split(path, "/models/")
					if len(parts) > 1 {
						modelName = strings.Split(parts[1], ":")[0]
					}
				}
				calls = append(calls, modelName)

				if modelName == "primary-model" {
					// Non-transient, model-specific/unrelated error (like 404 Model Not Found)
					respBody := `{"error": {"message": "Model not found"}}`
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Body:       io.NopCloser(bytes.NewBufferString(respBody)),
						Header:     make(http.Header),
					}, nil
				}

				if modelName == "fallback-model-1" {
					// Transient error
					respBody := `{"error": {"message": "Rate limit exceeded"}}`
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Body:       io.NopCloser(bytes.NewBufferString(respBody)),
						Header:     make(http.Header),
					}, nil
				}

				if modelName == "fallback-model-2" {
					// Success
					respBody := `{
						"candidates": [{
							"content": {
								"parts": [{"text": "TITLE: Success\nNOTES: Fallback worked\nTRANSCRIPT: NONE"}]
							}
						}],
						"usageMetadata": {
							"promptTokenCount": 10,
							"candidatesTokenCount": 5,
							"totalTokenCount": 15
						}
					}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(respBody)),
						Header:     make(http.Header),
					}, nil
				}

				return nil, fmt.Errorf("unexpected model: %s", modelName)
			},
		},
	}

	ctx := context.Background()
	media := []Media{{Data: []byte("fake"), MimeType: "image/jpeg"}}
	res, err := client.Identify(ctx, "fake-api-key", media, "test prompt")
	if err != nil {
		t.Fatalf("Identify failed: %v", err)
	}

	if res.Title != "Success" {
		t.Errorf("Expected title 'Success', got %q", res.Title)
	}
	if res.Model != "fallback-model-2" {
		t.Errorf("Expected model 'fallback-model-2', got %q", res.Model)
	}

	// We expect primary-model to be tried once (fails with 403, non-transient).
	// Then fallback-model-1 to be tried. Since 429 is transient (plain rate limit, no "quota"),
	// it should retry fallback-model-1. But wait, our mockTransport returns 429 without "quota" in body.
	// So IsTransient(err) is true. It will retry up to 3 times for fallback-model-1.
	// Then, after 3 attempts, it moves to fallback-model-2, which succeeds.
	// To avoid slow test, let's verify calls slice contains:
	// ["primary-model", "fallback-model-1", "fallback-model-1", "fallback-model-1", "fallback-model-2"]
	expectedCalls := []string{
		"primary-model",
		"fallback-model-1",
		"fallback-model-1",
		"fallback-model-1",
		"fallback-model-2",
	}

	if len(calls) != len(expectedCalls) {
		t.Errorf("Expected %d calls, got %d: %v", len(expectedCalls), len(calls), calls)
	}
	for i, c := range calls {
		if i < len(expectedCalls) && c != expectedCalls[i] {
			t.Errorf("Call %d: expected %q, got %q", i, expectedCalls[i], c)
		}
	}
}

func TestEmbedContentRetry(t *testing.T) {
	client := New()

	var attempts int
	client.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				attempts++
				if attempts < 3 {
					// Return transient 503
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       io.NopCloser(bytes.NewBufferString("Service Unavailable")),
						Header:     make(http.Header),
					}, nil
				}
				// Success
				respBody := `{
					"embedding": {
						"values": [0.1, 0.2, 0.3]
					},
					"usageMetadata": {
						"totalTokenCount": 5
					}
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(respBody)),
					Header:     make(http.Header),
				}, nil
			},
		},
	}

	ctx := context.Background()
	res, err := client.EmbedContent(ctx, "fake-key", "test text")
	if err != nil {
		t.Fatalf("EmbedContent failed: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
	if len(res.Vector) != 3 || res.Vector[0] != 0.1 {
		t.Errorf("Unexpected embedding vector: %v", res.Vector)
	}
}

func TestClientGlobalPermanentError(t *testing.T) {
	setTestConfig(t, []string{"fallback-model-1"})

	client := New()
	client.Model = "primary-model"

	var calls []string
	client.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				path := req.URL.Path
				modelName := ""
				if strings.Contains(path, "/models/") {
					parts := strings.Split(path, "/models/")
					if len(parts) > 1 {
						modelName = strings.Split(parts[1], ":")[0]
					}
				}
				calls = append(calls, modelName)

				// Return a global permanent error: 401 Unauthorized / API_KEY_INVALID
				respBody := `{"error": {"message": "API_KEY_INVALID"}}`
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Body:       io.NopCloser(bytes.NewBufferString(respBody)),
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

	// We expect primary-model to be tried once, and then abort immediately
	expectedCalls := []string{"primary-model"}
	if len(calls) != len(expectedCalls) {
		t.Errorf("Expected calls %v, got %v", expectedCalls, calls)
	}
}

type mockApprover struct {
	approved bool
	err      error
}

func (m *mockApprover) IsFailoverApproved(ctx context.Context, operation string) (bool, error) {
	return m.approved, m.err
}

func TestClientPaidFailover(t *testing.T) {
	setTestConfig(t, []string{})

	media := []Media{{Data: []byte("fake"), MimeType: "image/jpeg"}}
	ctx := context.Background()

	t.Run("Approved", func(t *testing.T) {
		client := New()
		client.Model = "primary-model"

		var keysUsed []string
		client.HTTP = &http.Client{
			Transport: &mockTransport{
				roundTrip: func(req *http.Request) (*http.Response, error) {
					key := req.URL.Query().Get("key")
					keysUsed = append(keysUsed, key)

					if key == "free-key" {
						// Quota exhausted error (429)
						respBody := `{"error": {"message": "Resource has been exhausted (e.g. queries per minute quota)"}}`
						return &http.Response{
							StatusCode: http.StatusTooManyRequests,
							Body:       io.NopCloser(bytes.NewBufferString(respBody)),
							Header:     make(http.Header),
						}, nil
					}

					if key == "paid-key" {
						respBody := `{
							"candidates": [{
								"content": {
									"parts": [{"text": "TITLE: Paid Success\nNOTES: Paid tier worked\nTRANSCRIPT: NONE"}]
								}
							}]
						}`
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(bytes.NewBufferString(respBody)),
							Header:     make(http.Header),
						}, nil
					}

					return nil, fmt.Errorf("unexpected key: %s", key)
				},
			},
		}

		approver := &mockApprover{approved: true}
		client.WithFailover("paid-key", approver, "identify-test")

		res, err := client.Identify(ctx, "free-key", media, "test prompt")
		if err != nil {
			t.Fatalf("Identify failed: %v", err)
		}

		if res.Title != "PaidSuccess" && !strings.Contains(res.Title, "Paid Success") {
			t.Errorf("Expected title 'Paid Success', got %q", res.Title)
		}

		expectedKeys := []string{"free-key", "paid-key"}
		if len(keysUsed) != len(expectedKeys) {
			t.Errorf("Expected keys %v, got %v", expectedKeys, keysUsed)
		}
		for i, k := range keysUsed {
			if k != expectedKeys[i] {
				t.Errorf("Key %d: expected %q, got %q", i, expectedKeys[i], k)
			}
		}
	})

	t.Run("NotApproved", func(t *testing.T) {
		client := New()
		client.Model = "primary-model"

		client.HTTP = &http.Client{
			Transport: &mockTransport{
				roundTrip: func(req *http.Request) (*http.Response, error) {
					respBody := `{"error": {"message": "quota exceeded"}}`
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Body:       io.NopCloser(bytes.NewBufferString(respBody)),
						Header:     make(http.Header),
					}, nil
				},
			},
		}

		approver := &mockApprover{approved: false}
		client.WithFailover("paid-key", approver, "identify-test")

		_, err := client.Identify(ctx, "free-key", media, "test prompt")
		if err == nil {
			t.Fatal("expected ErrFailoverApprovalRequired, got nil")
		}

		var failoverErr *ErrFailoverApprovalRequired
		if !errors.As(err, &failoverErr) {
			t.Errorf("expected ErrFailoverApprovalRequired, got error: %v", err)
		}
	})
}


