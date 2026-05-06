//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var notifySendWarnOnce sync.Once

// notifyDesktop sends a desktop notification on Linux via `notify-send`
// (libnotify). If notify-send isn't on PATH the message is logged to
// stderr and a one-time hint is printed.
//
// `link` is currently unused — most desktop environments don't make
// notify-send notifications clickable through a URL parameter. Future
// work could plug in dunst's `--action` mechanism or D-Bus directly.
func notifyDesktop(title, message, link string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		notifySendWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "notify: notify-send not found; install libnotify-bin to enable desktop notifications.")
		})
		fmt.Fprintf(os.Stderr, "[stash notify] %s: %s\n", title, message)
		return nil
	}
	out, err := exec.Command("notify-send", title, message).CombinedOutput()
	if err != nil {
		return fmt.Errorf("notify-send: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
