package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
	"github.com/msjurset/gostash/internal/usage"
)

func setTestConfig(t *testing.T, daily, monthly float64) {
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
max_monthly_budget_usd = %f
max_daily_budget_usd = %f
`, monthly, daily)

	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	config.Reload()
}

func TestServerBudgetGating(t *testing.T) {
	// Set low budgets
	setTestConfig(t, 0.01, 0.05)

	dir := t.TempDir()
	ledger := usage.New(dir)
	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := &Server{
		UsageLedger: ledger,
		Store:       db,
	}

	// Case 1: Budget not exceeded initially
	req := httptest.NewRequest("POST", "/ask", bytes.NewBufferString("invalid json"))
	w := httptest.NewRecorder()

	srv.handleAsk(w, req)
	if w.Code == http.StatusTooManyRequests {
		t.Errorf("Expected request to not be budget gated initially, but got 429")
	}

	// Case 2: Exceed daily budget
	// gemini-2.5-flash costs: input: 0.30 per million, output: 2.50 per million
	// We record 50k input tokens -> 50000 * 0.3 / 1e6 = 0.015 USD (exceeds 0.01 daily budget)
	ledger.Record("gemini-2.5-flash", 50000, 0)

	// Test handleAsk
	req = httptest.NewRequest("POST", "/ask", bytes.NewBufferString(`{"question": "test"}`))
	w = httptest.NewRecorder()
	srv.handleAsk(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected HTTP 429, got %d", w.Code)
	}
	if w.Body.String() != "budget-exceeded" {
		t.Errorf("Expected body 'budget-exceeded', got %q", w.Body.String())
	}

	// Test handleChat
	req = httptest.NewRequest("POST", "/items/123/chat", bytes.NewBufferString(`{"question": "test"}`))
	w = httptest.NewRecorder()
	srv.handleChat(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected HTTP 429, got %d", w.Code)
	}
	if w.Body.String() != "budget-exceeded" {
		t.Errorf("Expected body 'budget-exceeded', got %q", w.Body.String())
	}

	// Test handleAITextTransform
	req = httptest.NewRequest("POST", "/ai/fix", bytes.NewBufferString(`{"text": "test"}`))
	w = httptest.NewRecorder()
	srv.handleAITextTransform(w, req, "fix")

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected HTTP 429, got %d", w.Code)
	}
	if w.Body.String() != "budget-exceeded" {
		t.Errorf("Expected body 'budget-exceeded', got %q", w.Body.String())
	}

	// Test handleSearch (semantic = true, q = non-empty) -> should be gated and return 429 budget-exceeded
	req = httptest.NewRequest("GET", "/search?q=test&semantic=true", nil)
	w = httptest.NewRecorder()
	srv.handleSearch(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected HTTP 429, got %d", w.Code)
	}
	if w.Body.String() != "budget-exceeded" {
		t.Errorf("Expected body 'budget-exceeded', got %q", w.Body.String())
	}
}

func TestServerSyncUsageLogs(t *testing.T) {
	dir := t.TempDir()
	ledger := usage.New(dir)
	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := &Server{
		UsageLedger:   ledger,
		UsageRecorder: ledger,
		Store:         db,
	}

	logs := []model.UsageLog{
		{ID: "log-1", Model: "gemini-2.5-flash", PromptTokens: 100, CandidateTokens: 50, CreatedAt: time.Now().Add(-10 * time.Minute)},
		{ID: "log-2", Model: "gemini-2.5-flash", PromptTokens: 200, CandidateTokens: 100, CreatedAt: time.Now().Add(-5 * time.Minute)},
	}
	buf, err := json.Marshal(logs)
	if err != nil {
		t.Fatalf("failed to marshal logs: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/sync-logs", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.handleSyncUsageLogs(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d, body: %s", w.Code, w.Body.String())
	}

	// Verify that they are now recorded in the usage ledger (total prompt tokens = 300, candidate = 150)
	data, err := os.ReadFile(ledger.Path())
	if err != nil {
		t.Fatalf("failed to read ledger: %v", err)
	}

	var snap struct {
		Today struct {
			ByModel map[string]struct {
				Calls        int   `json:"calls"`
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"by_model"`
		} `json:"today"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to unmarshal ledger data: %v", err)
	}

	b := snap.Today.ByModel["gemini-2.5-flash"]
	if b.Calls != 2 {
		t.Errorf("expected 2 calls, got %d", b.Calls)
	}
	if b.InputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", b.InputTokens)
	}
	if b.OutputTokens != 150 {
		t.Errorf("expected 150 output tokens, got %d", b.OutputTokens)
	}

	// Sending duplicate logs should be ignored (deduplicated by RegisterUsageLogs)
	req2 := httptest.NewRequest("POST", "/api/sync-logs", bytes.NewReader(buf))
	w2 := httptest.NewRecorder()
	srv.handleSyncUsageLogs(w2, req2)

	if w2.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w2.Code)
	}

	// Ledger totals should remain unchanged (300 and 150) because they were ignored
	data2, _ := os.ReadFile(ledger.Path())
	var snap2 struct {
		Today struct {
			ByModel map[string]struct {
				Calls        int   `json:"calls"`
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"by_model"`
		} `json:"today"`
	}
	_ = json.Unmarshal(data2, &snap2)
	b2 := snap2.Today.ByModel["gemini-2.5-flash"]
	if b2.Calls != 2 {
		t.Errorf("expected Calls to remain 2, got %d", b2.Calls)
	}
	if b2.InputTokens != 300 {
		t.Errorf("expected InputTokens to remain 300, got %d", b2.InputTokens)
	}
}

