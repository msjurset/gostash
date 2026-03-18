package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/msjurset/gostash/internal/manpage"

	"github.com/spf13/cobra"
)

var manCmd = &cobra.Command{
	Use:   "man",
	Short: "Display the stash manual page",
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := os.CreateTemp("", "stash-man-*.1")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())

		if _, err := f.WriteString(manpage.Content); err != nil {
			f.Close()
			return err
		}
		f.Close()

		man := exec.Command("man", f.Name())
		man.Stdin = os.Stdin
		man.Stdout = os.Stdout
		man.Stderr = os.Stderr

		if err := man.Run(); err != nil {
			// Fallback: print raw content
			fmt.Print(manpage.Content)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(manCmd)
}
