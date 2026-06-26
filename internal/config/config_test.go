package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigThreadSafe(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	err := WriteDefault()
	if err != nil {
		t.Fatalf("failed to write default config: %v", err)
	}

	Reload()

	var wg sync.WaitGroup
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Get()
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			Reload()
		}(i)

		go func(idx int) {
			defer wg.Done()
			c := Get()
			c.MaxDailyBudgetUSD = float64(idx)
			_ = Save(c)
		}(i)
	}

	wg.Wait()
}

func TestConfigDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	configDir := filepath.Join(tmp, ".config", "stash")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	content := `
data_dir = "` + filepath.Join(tmp, "stash_data") + `"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	Reload()
	c := Get()

	if len(c.AIModels) != 1 || c.AIModels[0] != "gemini-2.5-flash" {
		t.Errorf("expected default AIModels [gemini-2.5-flash], got %v", c.AIModels)
	}
	if c.MaxVideoDurationMinutes != 30 {
		t.Errorf("expected default MaxVideoDurationMinutes 30, got %d", c.MaxVideoDurationMinutes)
	}
	if c.DataDir != filepath.Join(tmp, "stash_data") {
		t.Errorf("expected DataDir %s, got %s", filepath.Join(tmp, "stash_data"), c.DataDir)
	}
}

func TestConfigSave(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Reload()
	c := Get()
	c.AIModels = []string{"model-1", "model-2"}
	c.MaxDailyBudgetUSD = 5.0
	c.MaxMonthlyBudgetUSD = 100.0
	c.MaxVideoDurationMinutes = 15

	if err := Save(c); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	c2 := Get()
	if c2.MaxDailyBudgetUSD != 5.0 || c2.MaxMonthlyBudgetUSD != 100.0 || c2.MaxVideoDurationMinutes != 15 {
		t.Errorf("cached config not updated correctly after Save: %+v", c2)
	}
	if len(c2.AIModels) != 2 || c2.AIModels[0] != "model-1" || c2.AIModels[1] != "model-2" {
		t.Errorf("cached config AIModels not updated correctly after Save: %v", c2.AIModels)
	}

	Reload()
	c3 := Get()
	if c3.MaxDailyBudgetUSD != 5.0 || c3.MaxMonthlyBudgetUSD != 100.0 || c3.MaxVideoDurationMinutes != 15 {
		t.Errorf("reloaded config not updated correctly after Save: %+v", c3)
	}
}

func TestConfigExpansionAndSerialization(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	configDir := filepath.Join(tmp, ".config", "stash")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")

	// Test expansion of "~/" in paths
	content := `
data_dir = "~/my_stash_data"
db_path = "~/my_stash_data/stash.db"
files_dir = "~/my_stash_data/files"
backup_dir = "~/my_stash_data/backups"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	Reload()
	c := Get()

	expectedDataDir := filepath.Join(tmp, "my_stash_data")
	if c.DataDir != expectedDataDir {
		t.Errorf("expected expanded DataDir %q, got %q", expectedDataDir, c.DataDir)
	}
	expectedDBPath := filepath.Join(expectedDataDir, "stash.db")
	if c.DBPath != expectedDBPath {
		t.Errorf("expected expanded DBPath %q, got %q", expectedDBPath, c.DBPath)
	}
	expectedFilesDir := filepath.Join(expectedDataDir, "files")
	if c.FilesDir != expectedFilesDir {
		t.Errorf("expected expanded FilesDir %q, got %q", expectedFilesDir, c.FilesDir)
	}
	expectedBackupDir := filepath.Join(expectedDataDir, "backups")
	if c.BackupDir != expectedBackupDir {
		t.Errorf("expected expanded BackupDir %q, got %q", expectedBackupDir, c.BackupDir)
	}

	// Verify reload safety and TOML serialization format
	c.DataDir = "~/another_data"
	if err := Save(c); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Verify the physical file contains proper TOML format and is not empty
	serialized, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config file: %v", err)
	}
	serializedStr := string(serialized)
	if !strings.Contains(serializedStr, `data_dir = "~/another_data"`) {
		t.Errorf("expected saved config to contain data_dir serialization, got:\n%s", serializedStr)
	}
}
