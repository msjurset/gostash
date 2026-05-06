//go:build !darwin && !linux

package main

import (
	"fmt"
	"os"
)

// notifyDesktop on unsupported platforms (windows, *bsd, ...) just echoes
// to stderr. A real implementation could shell out to PowerShell on
// Windows or `osd_cat` on the *BSDs, but for now this is a stub.
func notifyDesktop(title, message, link string) error {
	if link != "" {
		fmt.Fprintf(os.Stderr, "[stash notify] %s: %s (%s)\n", title, message, link)
	} else {
		fmt.Fprintf(os.Stderr, "[stash notify] %s: %s\n", title, message)
	}
	return nil
}
