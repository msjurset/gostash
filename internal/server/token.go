// Package server hosts the HTTP API consumed by the Stash mobile
// companion (and any future client that wants to capture or browse
// over the network). Single bearer token auth, JSON responses,
// multipart upload for files/images.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenPath returns the canonical location of the bearer token.
// One token per stash install; rotate via `stash serve token --rotate`.
// File is mode 0600 — anyone with read access to it can hit the API,
// so we don't want it world-readable.
func TokenPath(stashDir string) string {
	return filepath.Join(stashDir, "serve.token")
}

// LoadOrCreateToken reads the bearer token from disk. If the file
// doesn't exist, generates a 32-byte random token, writes it, and
// returns it. First call from a fresh install kicks off the token —
// the user pairs the mobile app with this value.
func LoadOrCreateToken(stashDir string) (string, error) {
	path := TokenPath(stashDir)
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
		return "", errors.New("token file is empty; rotate it with `stash serve token --rotate`")
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read token: %w", err)
	}
	return RotateToken(stashDir)
}

// RotateToken generates a fresh token, writes it to disk (0600),
// and returns the new value. Previous token is overwritten — any
// device paired with it must re-pair.
func RotateToken(stashDir string) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(stashDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir stash dir: %w", err)
	}
	if err := os.WriteFile(TokenPath(stashDir), []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
