package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

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

	return openExternal(target)
}

func openTarget(item *model.Item) string {
	switch item.Type {
	case model.TypeLink:
		return item.URL
	case model.TypeFile, model.TypeImage:
		if item.StorePath != "" {
			fs := openFileStore()
			return fs.Path(item.StorePath)
		}
		if item.SourcePath != "" {
			return item.SourcePath
		}
	}
	return ""
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
