package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/store"
)

func setPaidTestConfig(t *testing.T, paidEnabled bool) {
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

	content := fmt.Sprintf(`
paid_tier_enabled = %t
`, paidEnabled)

	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	config.Reload()
}

func TestPaidTierGating(t *testing.T) {
	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	token := "test-bearer-token"
	srv := &Server{
		Store: db,
		Token: token,
		NewItemID: func() string {
			return "test-id"
		},
	}
	handler := srv.Handler()

	aiEndpoints := []struct {
		method string
		path   string
		body   string
	}{
		{"POST", "/items/123/chat", `{"question": "hello"}`},
		{"POST", "/ask", `{"question": "hello"}`},
		{"POST", "/ai/fix", `{"text": "hello"}`},
		{"POST", "/ai/summary", `{"text": "hello"}`},
		{"POST", "/ai/tags", `{"text": "hello"}`},
	}

	// Helper to send requests through Server.Handler
	sendReq := func(method, path, body string, headers map[string]string) (int, string) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	t.Run("Disabled", func(t *testing.T) {
		setPaidTestConfig(t, false)

		// Non-AI route should succeed (200 OK)
		code, _ := sendReq("GET", "/items", "", nil)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /items, got %d", code)
		}

		// Config route should succeed and return paid_tier_enabled = false
		code, body := sendReq("GET", "/config", "", nil)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /config, got %d", code)
		}
		var cfgRes map[string]any
		if err := json.Unmarshal([]byte(body), &cfgRes); err != nil {
			t.Fatalf("failed to parse /config JSON: %v", err)
		}
		if cfgRes["paid_tier_enabled"] != false {
			t.Errorf("Expected paid_tier_enabled to be false, got %v", cfgRes["paid_tier_enabled"])
		}

		// AI routes should NOT be blocked (i.e. should NOT return 402)
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, nil)
			if code == http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to not be blocked when paid tier is disabled, got %d with body %s", tc.method, tc.path, code, body)
			}
		}
	})

	t.Run("Enabled_NoPaymentInfo", func(t *testing.T) {
		setPaidTestConfig(t, true)

		// Non-AI route should succeed (200 OK)
		code, _ := sendReq("GET", "/items", "", nil)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /items, got %d", code)
		}

		// Config route should succeed and return paid_tier_enabled = true
		code, body := sendReq("GET", "/config", "", nil)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /config, got %d", code)
		}
		var cfgRes map[string]any
		if err := json.Unmarshal([]byte(body), &cfgRes); err != nil {
			t.Fatalf("failed to parse /config JSON: %v", err)
		}
		if cfgRes["paid_tier_enabled"] != true {
			t.Errorf("Expected paid_tier_enabled to be true, got %v", cfgRes["paid_tier_enabled"])
		}

		// AI routes should be blocked with 402
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, nil)
			if code != http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to be blocked with 402, got %d with body %s", tc.method, tc.path, code, body)
			}

			// Verify error structure
			var errRes map[string]string
			if err := json.Unmarshal([]byte(body), &errRes); err != nil {
				t.Errorf("Expected JSON error response, got: %s", body)
			} else {
				if errRes["error"] != "payment_required" {
					t.Errorf("Expected error 'payment_required', got %s", errRes["error"])
				}
				if errRes["message"] != "This feature requires a premium subscription." {
					t.Errorf("Unexpected message: %s", errRes["message"])
				}
			}
		}
	})

	t.Run("Enabled_WithHeader_StashPaid", func(t *testing.T) {
		setPaidTestConfig(t, true)

		headers := map[string]string{"X-Stash-Paid": "true"}

		// Non-AI route should succeed
		code, _ := sendReq("GET", "/items", "", headers)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /items, got %d", code)
		}

		// AI routes should NOT be blocked (NOT 402)
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, headers)
			if code == http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to be allowed with X-Stash-Paid: true, got %d: %s", tc.method, tc.path, code, body)
			}
		}
	})

	t.Run("Enabled_WithHeader_StashPaidTier", func(t *testing.T) {
		setPaidTestConfig(t, true)

		headers := map[string]string{"X-Stash-Paid-Tier": "true"}

		// Non-AI route should succeed
		code, _ := sendReq("GET", "/items", "", headers)
		if code != http.StatusOK {
			t.Errorf("Expected 200 for GET /items, got %d", code)
		}

		// AI routes should NOT be blocked (NOT 402)
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, headers)
			if code == http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to be allowed with X-Stash-Paid-Tier: true, got %d: %s", tc.method, tc.path, code, body)
			}
		}
	})

	t.Run("Enabled_IsPaidRequestHook", func(t *testing.T) {
		setPaidTestConfig(t, true)

		// Set hook to always return true
		srv.IsPaidRequest = func(r *http.Request) bool {
			return true
		}
		t.Cleanup(func() { srv.IsPaidRequest = nil })

		// AI routes should NOT be blocked
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, nil)
			if code == http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to be allowed by hook, got %d: %s", tc.method, tc.path, code, body)
			}
		}

		// Set hook to always return false
		srv.IsPaidRequest = func(r *http.Request) bool {
			return false
		}

		// AI routes should be blocked (even if headers are sent, since hook takes precedence and returns false)
		headers := map[string]string{"X-Stash-Paid": "true"}
		for _, tc := range aiEndpoints {
			code, body := sendReq(tc.method, tc.path, tc.body, headers)
			if code != http.StatusPaymentRequired {
				t.Errorf("Expected route %s %s to be blocked with 402 when hook returns false, got %d: %s", tc.method, tc.path, code, body)
			}
		}
	})
}
