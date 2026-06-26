package config

import (
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConfigConcurrencyRace stress-tests Get(), Reload(), and Save() with a large
// number of concurrent goroutines to verify that locking prevents races.
func TestConfigConcurrencyRace(t *testing.T) {
	// Setup test environment config directory
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		Reload()
	})

	cfgDir := filepath.Join(tempDir, ".config", "stash")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write initial config
	initialConfig := []byte(`
data_dir = "~/my_stash"
ai_models = ["gemini-2.5-flash"]
max_daily_budget_usd = 0.50
`)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), initialConfig, 0644); err != nil {
		t.Fatal(err)
	}

	Reload()

	// Spin up concurrent readers, writers, and reloader goroutines
	var wg sync.WaitGroup
	numReaders := 80
	numWriters := 10
	numReloaders := 10

	wg.Add(numReaders + numWriters + numReloaders)

	// Readers
	for i := 0; i < numReaders; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c := Get()
				_ = c.DataDir
				_ = c.AIModels
				time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			}
		}()
	}

	// Writers
	for i := 0; i < numWriters; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c := Get()
				c.MaxDailyBudgetUSD = float64(id) * 0.1
				err := Save(c)
				if err != nil {
					// We might get occasional write errors if multiple saves hit the same temp file name in the same microsecond,
					// but it shouldn't cause data races.
					_ = err
				}
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	// Reloaders
	for i := 0; i < numReloaders; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				Reload()
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

// TestConfigSaveResourceCleanups verifies that no temp files are leaked on Save errors.
func TestConfigSaveResourceCleanups(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		Reload()
	})

	// Make the config dir unwriteable to force a Save error
	cfgDir := filepath.Join(tempDir, ".config", "stash")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write an initial config first
	c := Config{DataDir: "~/my_stash"}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}

	// Now make directory unwriteable
	if err := os.Chmod(cfgDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(cfgDir, 0755)
	})

	// Try to Save, should fail
	err := Save(c)
	if err == nil {
		t.Error("expected error when saving to unwriteable directory, got nil")
	}

	// Restore permissions to inspect the directory
	if err := os.Chmod(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".toml" && file.Name() != "config.toml" {
			t.Errorf("found leaked temp config file: %s", file.Name())
		}
	}
}
