package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

// copyCmd is a small clipboard helper used by `stash find` keybinds
// (Ctrl-Y / Ctrl-I) and standalone scripts. Single responsibility:
// look up the item, extract one field, push it to the system
// clipboard. Avoids the `stash show | grep | sed | pbcopy` pipeline
// the find keybinds were doing — which silently emptied the clipboard
// when the regex didn't match.
var copyCmd = &cobra.Command{
	Use:   "copy <id>",
	Short: "Copy a field of a stashed item to the system clipboard",
	Long: `Copy one field of a stashed item to the clipboard. Used by 'stash find'
keybinds and handy in standalone scripts.

  stash copy <id>                 # default: URL (or extracted text if no URL)
  stash copy <id> --field url
  stash copy <id> --field id      # full ULID
  stash copy <id> --field title
  stash copy <id> --field content # extracted text / snippet body
  stash copy <id> --field notes`,
	Args: cobra.ExactArgs(1),
	RunE: runCopy,
}

func init() {
	copyCmd.Flags().String("field", "url", "Field to copy: url, id, title, content, notes")
	rootCmd.AddCommand(copyCmd)
}

func runCopy(cmd *cobra.Command, args []string) error {
	field, _ := cmd.Flags().GetString("field")

	value, err := resolveCopyValue(args[0], field)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("nothing to copy: field %q is empty on this item", field)
	}
	return writeClipboard(value)
}

func resolveCopyValue(itemID, field string) (string, error) {
	switch field {
	case "id":
		// "id" is a special case — we don't need to hit the store.
		return itemID, nil
	}
	s, err := openStore()
	if err != nil {
		return "", err
	}
	defer s.Close()
	item, err := s.GetItem(context.Background(), itemID)
	if err != nil {
		return "", err
	}
	switch field {
	case "url":
		// Fall back to extracted text for items without a URL
		// (snippets, untagged emails, etc.) so Ctrl-Y in the
		// finder always produces *something* useful.
		if item.URL != "" {
			return item.URL, nil
		}
		return fallbackContent(item), nil
	case "title":
		return item.Title, nil
	case "content":
		return fallbackContent(item), nil
	case "notes":
		return item.Notes, nil
	}
	return "", fmt.Errorf("unknown field %q (try: url, id, title, content, notes)", field)
}

func fallbackContent(item *model.Item) string {
	if item.ExtractedText != "" {
		return item.ExtractedText
	}
	if item.Notes != "" {
		return item.Notes
	}
	return item.Title
}

func writeClipboard(s string) error {
	bin, args := clipboardCommand()
	if bin == "" {
		return fmt.Errorf("no clipboard tool available on this platform")
	}
	c := exec.Command(bin, args...)
	c.Stdin = strings.NewReader(s)
	return c.Run()
}

// clipboardCommand returns the binary + args for piping stdin to the
// system clipboard. Splits the command so we can pass arguments
// without round-tripping through a shell.
func clipboardCommand() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy", nil
	case "linux":
		for _, candidate := range [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		} {
			if _, err := exec.LookPath(candidate[0]); err == nil {
				return candidate[0], candidate[1:]
			}
		}
	case "windows":
		return "clip", nil
	}
	return "", nil
}
