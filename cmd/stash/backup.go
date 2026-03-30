package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create a backup of the stash database and files",
	Long: `Create a timestamped backup of the stash database and content files.

  stash backup              # full backup (DB + files)
  stash backup --db-only    # database only (faster)
  stash backup --list       # list available backups`,
	RunE: runBackup,
}

var restoreCmd = &cobra.Command{
	Use:   "restore [backup-name]",
	Short: "Restore from a backup",
	Long: `Restore the stash database and files from a backup.

  stash restore                              # restore from latest backup
  stash restore stash-20260329-120000.db     # restore specific backup
  stash restore /path/to/backup.db           # restore from absolute path`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRestore,
}

func init() {
	backupCmd.Flags().Bool("list", false, "List available backups")
	backupCmd.Flags().Bool("db-only", false, "Back up database only (skip files)")
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(restoreCmd)
}

func runBackup(cmd *cobra.Command, args []string) error {
	listMode, _ := cmd.Flags().GetBool("list")
	if listMode {
		return listBackups()
	}

	dbOnly, _ := cmd.Flags().GetBool("db-only")

	// Ensure backup directory exists
	backupDir := config.BackupDir()
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	// Open store and checkpoint WAL
	s, err := openStore()
	if err != nil {
		return err
	}
	if err := s.Checkpoint(); err != nil {
		s.Close()
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	s.Close()

	timestamp := time.Now().Format("20060102-150405")

	// Copy database
	dbSrc := config.DBPath()
	dbDst := filepath.Join(backupDir, fmt.Sprintf("stash-%s.db", timestamp))
	if err := copyFileSync(dbSrc, dbDst); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}

	dbInfo, _ := os.Stat(dbDst)
	dbSize := dbInfo.Size()

	if !flagJSON {
		fmt.Printf("Database backed up: %s (%s)\n", filepath.Base(dbDst), humanSize(dbSize))
	}

	// Archive files directory
	var filesSize int64
	if !dbOnly {
		filesDir := config.FilesDir()
		if fi, err := os.Stat(filesDir); err == nil && fi.IsDir() {
			filesDst := filepath.Join(backupDir, fmt.Sprintf("stash-%s-files.tar.gz", timestamp))
			size, err := archiveDir(filesDir, filesDst)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: file store backup failed: %v\n", err)
			} else {
				filesSize = size
				if !flagJSON {
					fmt.Printf("Files backed up:    %s (%s)\n", filepath.Base(filesDst), humanSize(filesSize))
				}
			}
		}
	}

	if flagJSON {
		result := map[string]any{
			"timestamp":  timestamp,
			"db_path":    dbDst,
			"db_size":    dbSize,
			"files_size": filesSize,
			"db_only":    dbOnly,
		}
		printJSON(result)
	} else {
		fmt.Printf("Backup complete: %s\n", backupDir)
	}

	return nil
}

func listBackups() error {
	backupDir := config.BackupDir()

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No backups found.")
			return nil
		}
		return fmt.Errorf("read backup dir: %w", err)
	}

	type backupInfo struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}

	var backups []backupInfo
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			info, _ := e.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			// Check for companion files archive
			filesArchive := strings.TrimSuffix(e.Name(), ".db") + "-files.tar.gz"
			filesPath := filepath.Join(backupDir, filesArchive)
			if fi, err := os.Stat(filesPath); err == nil {
				size += fi.Size()
			}
			backups = append(backups, backupInfo{
				Name: e.Name(),
				Path: filepath.Join(backupDir, e.Name()),
				Size: size,
			})
		}
	}

	// Sort newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name > backups[j].Name
	})

	if flagJSON {
		if backups == nil {
			backups = []backupInfo{}
		}
		printJSON(backups)
		return nil
	}

	if len(backups) == 0 {
		fmt.Println("No backups found.")
		return nil
	}

	fmt.Printf("Backups in %s:\n\n", backupDir)
	for _, b := range backups {
		fmt.Printf("  %-40s %s\n", b.Name, humanSize(b.Size))
	}
	fmt.Printf("\n%d backup(s)\n", len(backups))
	return nil
}

func runRestore(cmd *cobra.Command, args []string) error {
	backupDir := config.BackupDir()
	var dbBackupPath string

	if len(args) == 1 {
		name := args[0]
		if filepath.IsAbs(name) {
			dbBackupPath = name
		} else {
			dbBackupPath = filepath.Join(backupDir, name)
		}
	} else {
		// Find latest backup
		path, err := latestBackup(backupDir)
		if err != nil {
			return err
		}
		dbBackupPath = path
	}

	if _, err := os.Stat(dbBackupPath); err != nil {
		return fmt.Errorf("backup not found: %s", dbBackupPath)
	}

	dbPath := config.DBPath()

	// Restore database
	if err := copyFileSync(dbBackupPath, dbPath); err != nil {
		return fmt.Errorf("restore database: %w", err)
	}
	// Clean up WAL/SHM
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	dbInfo, _ := os.Stat(dbPath)

	// Check for companion files archive
	filesArchivePath := strings.TrimSuffix(dbBackupPath, ".db") + "-files.tar.gz"
	var filesRestored bool
	if _, err := os.Stat(filesArchivePath); err == nil {
		filesDir := config.FilesDir()
		if err := extractArchive(filesArchivePath, filesDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: file store restore failed: %v\n", err)
		} else {
			filesRestored = true
		}
	}

	if flagJSON {
		result := map[string]any{
			"restored_from":  dbBackupPath,
			"database":       dbPath,
			"db_size":        dbInfo.Size(),
			"files_restored": filesRestored,
		}
		printJSON(result)
	} else {
		fmt.Printf("Restored from: %s (%s)\n", filepath.Base(dbBackupPath), humanSize(dbInfo.Size()))
		fmt.Printf("Database:      %s\n", dbPath)
		if filesRestored {
			fmt.Printf("Files:         restored\n")
		}
	}

	return nil
}

func latestBackup(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read backup dir: %w", err)
	}

	var latest string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") && strings.HasPrefix(e.Name(), "stash-") {
			if e.Name() > latest {
				latest = e.Name()
			}
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no backups found in %s", dir)
	}
	return filepath.Join(dir, latest), nil
}

func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func archiveDir(srcDir, dstPath string) (int64, error) {
	f, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	baseDir := filepath.Base(srcDir)
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		name := filepath.Join(baseDir, rel)

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(name)

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
	if err != nil {
		return 0, err
	}

	// Close writers to flush
	tw.Close()
	gw.Close()
	f.Sync()

	fi, err := os.Stat(dstPath)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func extractArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip the top-level "files" directory from the archive path
		// to restore directly into destDir
		parts := strings.SplitN(filepath.Clean(hdr.Name), string(filepath.Separator), 2)
		if len(parts) < 2 {
			continue // skip the root dir entry itself
		}
		rel := parts[1]
		target := filepath.Join(destDir, rel)

		// Safety: prevent path traversal
		if !strings.HasPrefix(target, destDir) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			io.Copy(out, tr)
			out.Close()
		}
	}
	return nil
}

