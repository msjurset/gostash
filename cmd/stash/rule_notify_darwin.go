//go:build darwin

package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

var terminalNotifierWarnOnce sync.Once

// notifyDesktop sends a desktop notification on macOS. When `link` is set,
// terminal-notifier is preferred so the banner is clickable — clicking it
// opens the URL (for link items) or file (for file items) in its default
// app. Without terminal-notifier we fall back to osascript's `display
// notification`, which is non-clickable but bundled with every macOS.
//
// Mirrors Sortie's notify pattern. Install with `brew install
// terminal-notifier` to enable click-through.
func notifyDesktop(title, message, link string) error {
	if link != "" {
		if path, err := exec.LookPath("terminal-notifier"); err == nil {
			args := []string{"-title", title, "-message", message, "-open", link}
			out, err := exec.Command(path, args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("terminal-notifier: %s: %w", strings.TrimSpace(string(out)), err)
			}
			return nil
		}
		terminalNotifierWarnOnce.Do(func() {
			log.Printf("notify: clickable notifications require terminal-notifier on macOS; install with `brew install terminal-notifier`. Falling back to plain notification.")
		})
	}

	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
