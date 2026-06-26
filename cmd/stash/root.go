package main

import (
	"fmt"
	"os"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var (
	flagJSON bool
	flagDB   string
)

var rootCmd = &cobra.Command{
	Use:     "stash",
	Short:   "Personal knowledge vault",
	Long:    "Capture, organize, and search anything — URLs, text snippets, files, images.",
	Version: "dev-trace-99",
	RunE:    runTUI,
	// Don't dump the cobra Usage block when a RunE returns an error.
	// In CLI usage cobra's default usage-on-error is helpful, but
	// stash-mac surfaces stderr as a user-facing alert and the
	// usage spam (Flags:/Global Flags:/etc.) hijacks the message.
	// Errors are still printed; we just suppress the help dump.
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Output as JSON")
	rootCmd.PersistentFlags().StringVar(&flagDB, "db", "", "Database path (default: ~/.stash/stash.db)")
}

func openStore() (store.Store, error) {
	if err := config.EnsureDir(); err != nil {
		return nil, fmt.Errorf("create stash dir: %w", err)
	}

	dsn := flagDB
	if dsn == "" {
		dsn = config.DBPath()
	}

	return store.NewSQLite(dsn)
}

func openFileStore() *filestore.FileStore {
	dir := config.FilesDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: create files dir: %v\n", err)
	}
	return filestore.New(dir)
}
