// Package credentials provides a thin cross-platform wrapper around
// the system keychain for caching secrets that originate in 1Password.
// Modeled directly on goback's `internal/credentials` — same shape,
// same `LoadOrResolve` behavior — so the user-side mental model is
// unchanged: "secrets live in 1Password; the keychain is a cache that
// lets launchd-spawned daemons read them without GUI interaction."
//
// Why not just shell out to `op read` on every call? Because the
// `stash serve` daemon runs under launchd and has no GUI context: a
// TouchID prompt from `op` would never reach the user. The keychain
// cache is the bridge — the user resolves once interactively (via
// `stash auth set-gemini`), and the daemon reads silently from the
// cache for the lifetime of that cache entry.
package credentials

import (
	"fmt"
	"os/exec"
	"strings"
)

// serviceName is the per-app namespace inside the OS keyring. Choosing
// "stash" (not "gostash") so the entries show up under a name the user
// recognizes when browsing Keychain Access.
const serviceName = "stash"

// Well-known keychain item names. Centralized so the daemon and the
// auth CLI agree on the same string without re-deriving it.
const (
	KeyGeminiAPIKey = "gemini-api-key"
)

// Store saves a secret to the platform keychain under the given key.
func Store(key, value string) error {
	return platformStore(key, value)
}

// Load retrieves a secret from the platform keychain by key. Returns
// empty string and nil error if the key doesn't exist — callers
// distinguish "not cached" from "error" by checking len(val) > 0.
func Load(key string) (string, error) {
	return platformLoad(key)
}

// Delete removes a secret from the platform keychain by key.
func Delete(key string) error {
	return platformDelete(key)
}

// ResolveOp calls `op read` to resolve a 1Password secret reference
// like "op://Personal/Gemini/credential". Surfaces stderr from op so
// the user sees the real reason ("not signed in", "item not found",
// etc.) rather than a generic exit-code error.
func ResolveOp(ref string) (string, error) {
	out, err := exec.Command("op", "read", ref).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("op read %s: %s", ref, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveAndCache resolves an op:// reference via 1Password CLI,
// stores the result in the platform keychain, and returns the value.
// The interactive TouchID prompt happens during the op call.
func ResolveAndCache(key, opRef string) (string, error) {
	val, err := ResolveOp(opRef)
	if err != nil {
		return "", err
	}
	if err := Store(key, val); err != nil {
		return "", fmt.Errorf("storing in keychain: %w", err)
	}
	return val, nil
}

// LoadOrResolve tries the platform keychain first, falling back to
// `op read` (and caching the result) when nothing is cached. Used by
// the daemon at startup so it can run unattended after a one-time
// interactive resolve, and re-prime itself silently if `op` happens
// to be reachable when the cache is empty.
func LoadOrResolve(key, opRef string) (string, error) {
	val, err := Load(key)
	if err != nil {
		return "", err
	}
	if val != "" {
		return val, nil
	}
	return ResolveAndCache(key, opRef)
}
