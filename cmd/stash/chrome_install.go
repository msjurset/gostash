package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

const nativeHostName = "com.gostash.host"

var chromeInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Register the native messaging host with Chrome",
	Long: `Writes the Native Messaging manifest so Chrome can find the stash host.

After running this command, load the extension in Chrome:
  1. Open chrome://extensions
  2. Enable "Developer mode"
  3. Click "Load unpacked"
  4. Select the chrome-extension/ directory from the gostash source`,
	RunE: runChromeInstall,
}

var chromeUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the native messaging host manifest",
	RunE:  runChromeUninstall,
}

func init() {
	chromeHostCmd.AddCommand(chromeInstallCmd)
	chromeHostCmd.AddCommand(chromeUninstallCmd)
}

type nativeManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func runChromeInstall(cmd *cobra.Command, args []string) error {
	// Find the stash binary
	binaryPath, err := exec.LookPath("stash")
	if err != nil {
		// Fall back to the currently running binary
		binaryPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("could not find stash binary: %w", err)
		}
	}
	binaryPath, _ = filepath.Abs(binaryPath)

	// Create a wrapper script that calls `stash chrome-host`
	wrapperDir := filepath.Join(homeDir(), ".stash")
	os.MkdirAll(wrapperDir, 0755)
	wrapperPath := filepath.Join(wrapperDir, "chrome-host.sh")

	wrapper := fmt.Sprintf("#!/bin/sh\nexec %q chrome-host \"$@\"\n", binaryPath)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0755); err != nil {
		return fmt.Errorf("write wrapper script: %w", err)
	}

	manifest := nativeManifest{
		Name:        nativeHostName,
		Description: "Stash - Personal Knowledge Vault",
		Path:        wrapperPath,
		Type:        "stdio",
		AllowedOrigins: []string{
			"chrome-extension://gpimkdoecfppbfllofdmlffheniooelb/",
		},
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	manifestDir := filepath.Join(homeDir(), "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}

	manifestPath := filepath.Join(manifestDir, nativeHostName+".json")
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("Native messaging host installed:\n")
	fmt.Printf("  Manifest: %s\n", manifestPath)
	fmt.Printf("  Host:     %s\n", wrapperPath)
	fmt.Printf("  Binary:   %s\n", binaryPath)
	fmt.Printf("\nNext: load the Chrome extension from chrome://extensions\n")
	return nil
}

func runChromeUninstall(cmd *cobra.Command, args []string) error {
	manifestPath := filepath.Join(homeDir(), "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts", nativeHostName+".json")
	wrapperPath := filepath.Join(homeDir(), ".stash", "chrome-host.sh")

	removed := false
	if err := os.Remove(manifestPath); err == nil {
		fmt.Printf("Removed manifest: %s\n", manifestPath)
		removed = true
	}
	if err := os.Remove(wrapperPath); err == nil {
		fmt.Printf("Removed wrapper: %s\n", wrapperPath)
		removed = true
	}

	if !removed {
		fmt.Println("Nothing to remove — native messaging host not installed.")
	}
	return nil
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
