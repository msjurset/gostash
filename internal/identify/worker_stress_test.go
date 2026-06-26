package identify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
	"github.com/msjurset/gostash/internal/usage"
)

type mockTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func setLedgerNow(l *usage.Ledger, now func() time.Time) {
	val := reflect.ValueOf(l).Elem()
	field := val.FieldByName("now")
	ptr := unsafe.Pointer(field.UnsafeAddr())
	*(*func() time.Time)(ptr) = now
}

// setupMockKeyChainAndFFprobe sets up dummy security and ffprobe scripts in PATH.
func setupMockKeyChainAndFFprobe(t *testing.T) string {
	tempBinDir := t.TempDir()

	securityScript := fmt.Sprintf(`#!/bin/bash
KEY_FILE="%s/key.txt"
CMD=$1
shift

if [ "$CMD" = "find-generic-password" ]; then
    if [ -f "$KEY_FILE" ]; then
        cat "$KEY_FILE"
        exit 0
    else
        echo "security: generic password could not be found" >&2
        exit 1
    fi
elif [ "$CMD" = "add-generic-password" ]; then
    while [ "$#" -gt 0 ]; do
        case "$1" in
            -w)
                echo "$2" > "$KEY_FILE"
                shift 2
                ;;
            *)
                shift
                ;;
        esac
    done
    exit 0
elif [ "$CMD" = "delete-generic-password" ]; then
    rm -f "$KEY_FILE"
    exit 0
else
    echo "Unknown command: $CMD" >&2
    exit 1
fi
`, tempBinDir)

	ffprobeScript := `#!/bin/bash
if [ "$MOCK_FFPROBE_FAIL" = "1" ]; then
    echo "ffprobe mock failure" >&2
    exit 1
fi
if [ -n "$MOCK_FFPROBE_DURATION" ]; then
    echo "$MOCK_FFPROBE_DURATION"
    exit 0
fi
echo "10.0"
exit 0
`

	if err := os.WriteFile(filepath.Join(tempBinDir, "security"), []byte(securityScript), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempBinDir, "ffprobe"), []byte(ffprobeScript), 0755); err != nil {
		t.Fatal(err)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", tempBinDir+":"+origPath)
	t.Cleanup(func() {
		os.Setenv("PATH", origPath)
	})

	// Pre-populate KeyChain with a mock API key
	if err := os.WriteFile(filepath.Join(tempBinDir, "key.txt"), []byte("mock-gemini-key"), 0644); err != nil {
		t.Fatal(err)
	}

	return tempBinDir
}

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

// TestWorkerBudgetAndIngestionCost verifies that background workers stop making API calls
// when budget is exceeded and resume cleanly when budget rolls over.
func TestWorkerBudgetAndIngestionCost(t *testing.T) {
	setupMockKeyChainAndFFprobe(t)

	// Set low daily budget (0.01 USD)
	setTestConfig(t, 0.01, 10.0)

	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	fsDir := t.TempDir()
	fstore := filestore.New(fsDir)

	// Add an item that needs identify
	item := model.Item{
		ID:        "item-1",
		Title:     "IMG_123.jpg",
		Type:      model.TypeImage,
		MimeType:  "image/jpeg",
		CreatedAt: time.Now(),
	}
	// Write dummy blob to filestore
	blobData := []byte("fake image data")
	hash, _, err := fstore.Save(bytes.NewReader(blobData))
	if err != nil {
		t.Fatal(err)
	}
	item.ContentHash = hash

	if err := db.CreateItem(context.Background(), &item); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTag(context.Background(), "item-1", Tag); err != nil {
		t.Fatal(err)
	}

	// Instantiate usage ledger
	ledgerDir := t.TempDir()
	ledger := usage.New(ledgerDir)
	day1 := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	setLedgerNow(ledger, func() time.Time { return day1 })

	gemClient := gemini.New()
	var apiCalls int
	gemClient.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				apiCalls++
				respBody := `{
					"candidates": [{
						"content": {
							"parts": [{"text": "TITLE: Identified Image\nNOTES: A beautiful mushroom\nTRANSCRIPT: NONE"}]
						}
					}],
					"usageMetadata": {
						"promptTokenCount": 100,
						"candidatesTokenCount": 50,
						"totalTokenCount": 150
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

	opts := Options{
		Recorder:     ledger,
		PollInterval: 1 * time.Second,
		Logger:       log.New(os.Stdout, "", 0),
	}

	w := New(db, fstore, gemClient, opts)

	// Force daily budget to be exceeded beforehand on ledger (cost of 50000 input tokens = 0.015 USD > 0.01 USD daily budget)
	ledger.Record("gemini-2.5-flash", 50000, 0)

	// Run worker tick
	w.tick(context.Background())

	// Because budget is exceeded, worker should return immediately and not make any API calls.
	if apiCalls != 0 {
		t.Errorf("Expected 0 API calls when budget is exceeded, got %d", apiCalls)
	}

	// Confirm it is still tagged
	dbItem, err := db.GetItem(context.Background(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if !dbItem.HasTag(Tag) {
		t.Error("expected item to still have needs-identify tag")
	}

	// Now roll over time to next day
	day2 := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	setLedgerNow(ledger, func() time.Time { return day2 })

	// Run worker tick again
	w.tick(context.Background())

	// Because date rolled over, budget usage is reset and API call should proceed
	if apiCalls != 1 {
		t.Errorf("Expected 1 API call after budget rollover, got %d", apiCalls)
	}

	// Confirm needs-identify tag was removed
	dbItem, err = db.GetItem(context.Background(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if dbItem.HasTag(Tag) {
		t.Error("expected needs-identify tag to be removed after successful identification")
	}
}

// TestWorkerFailClosedDurationCheck verifies that the worker fails closed when ffprobe duration check fails.
func TestWorkerFailClosedDurationCheck(t *testing.T) {
	setupMockKeyChainAndFFprobe(t)

	db, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	fsDir := t.TempDir()
	fstore := filestore.New(fsDir)

	// Add a video item
	item := model.Item{
		ID:        "item-video",
		Title:     "my_video.mp4",
		Type:      model.TypeImage, // Wait, type must be image (or video is checked)
		MimeType:  "video/mp4",
		CreatedAt: time.Now(),
	}
	blobData := []byte("corrupt video data")
	hash, _, err := fstore.Save(bytes.NewReader(blobData))
	if err != nil {
		t.Fatal(err)
	}
	item.ContentHash = hash

	if err := db.CreateItem(context.Background(), &item); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTag(context.Background(), "item-video", Tag); err != nil {
		t.Fatal(err)
	}

	gemClient := gemini.New()
	var apiCalls int
	gemClient.HTTP = &http.Client{
		Transport: &mockTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				apiCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("{}")),
				}, nil
			},
		},
	}

	opts := Options{
		PollInterval: 1 * time.Second,
		MaxAttempts:  2,
		Logger:       log.New(os.Stdout, "", 0),
	}

	w := New(db, fstore, gemClient, opts)

	// Simulate ffprobe failure
	os.Setenv("MOCK_FFPROBE_FAIL", "1")
	defer os.Unsetenv("MOCK_FFPROBE_FAIL")

	// Run tick 1
	w.tick(context.Background())

	// Duration check failed, should FAIL CLOSED (no API calls)
	if apiCalls != 0 {
		t.Errorf("Expected 0 API calls on fail-closed duration check, got %d", apiCalls)
	}

	// Item should increment attempts to 1, but still have needs-identify
	dbItem, err := db.GetItem(context.Background(), "item-video")
	if err != nil {
		t.Fatal(err)
	}
	if !dbItem.HasTag(Tag) {
		t.Error("expected needs-identify tag to remain after 1 failure")
	}

	// Run tick 2 to exhaust max attempts (MaxAttempts is 2)
	w.tick(context.Background())

	// Item should be untagged from needs-identify and tagged with identify-failed
	dbItem, err = db.GetItem(context.Background(), "item-video")
	if err != nil {
		t.Fatal(err)
	}
	if dbItem.HasTag(Tag) {
		t.Error("expected needs-identify tag to be removed after MaxAttempts")
	}
	if !dbItem.HasTag("identify-failed") {
		t.Error("expected item to be tagged with identify-failed after MaxAttempts")
	}
}

// TestWorkerDurationCheckResourceCleanup verifies that temp files created during
// duration check are cleaned up even on errors or when check runs successfully.
func TestWorkerDurationCheckResourceCleanup(t *testing.T) {
	setupMockKeyChainAndFFprobe(t)

	// Get system temp directory
	tempDir := os.TempDir()

	// Helper to count files matching stash-duration-*
	countTempFiles := func() int {
		files, err := os.ReadDir(tempDir)
		if err != nil {
			return 0
		}
		var count int
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "stash-duration-") {
				count++
			}
		}
		return count
	}

	// Clean any pre-existing temp files to be precise
	files, _ := os.ReadDir(tempDir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "stash-duration-") {
			_ = os.Remove(filepath.Join(tempDir, f.Name()))
		}
	}

	initialCount := countTempFiles()
	if initialCount != 0 {
		t.Fatalf("Pre-existing temp files could not be cleaned up: %d found", initialCount)
	}

	// Case 1: ffprobe succeeds
	os.Setenv("MOCK_FFPROBE_DURATION", "45.0")
	dur, err := getVideoDuration([]byte("mock video content"))
	os.Unsetenv("MOCK_FFPROBE_DURATION")
	if err != nil {
		t.Fatal(err)
	}
	if dur != 45*time.Second {
		t.Errorf("Expected 45s duration, got %v", dur)
	}

	afterSuccessCount := countTempFiles()
	if afterSuccessCount != 0 {
		t.Errorf("Leaked temp files after success: %d found", afterSuccessCount)
	}

	// Case 2: ffprobe fails
	os.Setenv("MOCK_FFPROBE_FAIL", "1")
	_, err = getVideoDuration([]byte("mock video content"))
	os.Unsetenv("MOCK_FFPROBE_FAIL")
	if err == nil {
		t.Error("expected error, got nil")
	}

	afterFailureCount := countTempFiles()
	if afterFailureCount != 0 {
		t.Errorf("Leaked temp files after failure: %d found", afterFailureCount)
	}
}
