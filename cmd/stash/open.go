package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:   "open <id>",
	Short: "Open a stashed item in its default application",
	Args:  cobra.ExactArgs(1),
	RunE:  runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	item, err := s.GetItem(context.Background(), args[0])
	if err != nil {
		return err
	}

	target := openTarget(item)
	if target == "" {
		return fmt.Errorf("nothing to open for this item")
	}

	// Use configured image viewer for image items
	if item.Type == model.TypeImage {
		if viewer := config.Get().ImageViewer; viewer != "" {
			return exec.Command(viewer, target).Start()
		}
	}

	return openExternal(target)
}

func openTarget(item *model.Item) string {
	switch item.Type {
	case model.TypeURL:
		return item.URL
	case model.TypeFile, model.TypeImage, model.TypeEmail:
		if item.StorePath != "" {
			fs := openFileStore()
			storePath := fs.Path(item.StorePath)
			// Copy to a temp file with the correct extension so macOS
			// opens the file with the right application
			ext := extFromMIMEOrSource(item.MimeType, item.SourcePath)
			if ext != "" {
				tmpFile := filepath.Join(os.TempDir(), "stash-open-"+item.StorePath[:8]+ext)
				if err := copyFile(storePath, tmpFile); err == nil {
					return tmpFile
				}
			}
			return storePath
		}
		if item.SourcePath != "" {
			return item.SourcePath
		}
	}
	return ""
}

func extFromMIMEOrSource(mimeType, sourcePath string) string {
	// Prefer original source extension
	if sourcePath != "" {
		if ext := filepath.Ext(sourcePath); ext != "" {
			return ext
		}
	}
	// Fall back to MIME type
	switch {
	case mimeType == "application/pdf":
		return ".pdf"
	case mimeType == "text/html":
		return ".html"
	case mimeType == "text/plain":
		return ".txt"
	case mimeType == "image/png":
		return ".png"
	case mimeType == "image/jpeg":
		return ".jpg"
	case mimeType == "image/gif":
		return ".gif"
	case mimeType == "image/webp":
		return ".webp"
	case mimeType == "application/gzip":
		return ".tar.gz"
	case mimeType == "application/zip":
		return ".zip"
	default:
		return ""
	}
}

func copyFile(src, dst string) error {
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
	_, err = io.Copy(out, in)
	return err
}

func openExternal(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", target)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
