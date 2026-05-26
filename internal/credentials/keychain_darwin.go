package credentials

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// trustedApps returns the application paths that should be granted
// keychain ACL access to items this process creates. By default: the
// running `stash` binary (so the launchd-spawned `stash serve`
// daemon can read cached secrets without a GUI prompt), plus
// /usr/bin/security for admin introspection. Additional paths can be
// added via STASH_KEYCHAIN_TRUST (colon-separated) when stash is
// installed in more than one location — e.g. ~/.local/bin/stash for
// interactive use AND /usr/local/bin/stash for the daemon.
func trustedApps() []string {
	apps := []string{"/usr/bin/security"}
	if self, err := os.Executable(); err == nil && self != "" {
		apps = append(apps, self)
	}
	if extra := os.Getenv("STASH_KEYCHAIN_TRUST"); extra != "" {
		for _, p := range strings.Split(extra, ":") {
			if p != "" {
				apps = append(apps, p)
			}
		}
	}
	return apps
}

func platformStore(key, value string) error {
	// Delete first so the item is re-created with a fresh ACL; -U
	// updates the password value but leaves the existing ACL in
	// place, which means a binary path change (e.g. brew updating
	// /opt/homebrew/bin/stash) silently breaks daemon reads.
	_ = platformDelete(key)

	args := []string{
		"add-generic-password",
		"-s", serviceName,
		"-a", key,
		"-w", value,
		"-U",
	}
	for _, app := range trustedApps() {
		args = append(args, "-T", app)
	}

	cmd := exec.Command("security", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(key string) (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", serviceName,
		"-a", key,
		"-w",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Distinguish "no such item" (a normal cache-miss outcome)
		// from access errors like "User interaction is not allowed."
		// which happen when called from a launchd session that lacks
		// keychain access. Surfacing access errors is what lets the
		// caller decide whether to fall back or fail loudly; a blanket
		// empty-on-error would mask daemon misconfiguration as "secret
		// not cached, run auth again."
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "could not be found") {
			return "", nil
		}
		if msg == "" {
			return "", fmt.Errorf("keychain load: %w", err)
		}
		return "", fmt.Errorf("keychain load: %s: %w", msg, err)
	}

	result := strings.TrimSpace(string(out))

	// macOS Keychain hex-encodes values that contain newlines or
	// non-ASCII bytes. Detect and decode.
	if isHexEncoded(result) {
		decoded, err := hex.DecodeString(result)
		if err != nil {
			return "", fmt.Errorf("decoding hex keychain value: %w", err)
		}
		return string(decoded), nil
	}

	return result, nil
}

// isHexEncoded checks if a string looks like hex-encoded data from
// Keychain. Keychain hex output is lowercase hex with no separators.
// Long-form heuristic to avoid false-positives on short hex-looking
// strings — Gemini keys (AIza…) are 39 chars and wouldn't naturally
// be all-hex, but the safer bet is "hex-decode only when it's a
// plausible binary blob length".
func isHexEncoded(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 64
}

func platformDelete(key string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", serviceName,
		"-a", key,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
