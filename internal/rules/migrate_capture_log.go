package rules

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// migrateLegacyRulesLog folds the previous-name `rules.log` into the
// renamed `capture.log`. The on-disk format is identical (JSONL, one
// Event per line) so this is a straight byte-for-byte append rather
// than a parsed re-emit. The legacy file is removed on success.
//
// Triggered transparently by AppendEvent — see logging.go. Idempotent
// across restarts: once the legacy file is removed, the migration
// short-circuits on every subsequent call.
func migrateLegacyRulesLog(legacyPath, newPath string) error {
	in, err := os.Open(legacyPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", legacyPath, err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.OpenFile(newPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", newPath, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s -> %s: %w", legacyPath, newPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", newPath, err)
	}

	if err := os.Remove(legacyPath); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: migrated %s but couldn't remove it: %v\n",
			legacyPath, err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
