package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// findListCmd is the data-only sibling of `stash find` — same filter
// flags, but outputs the tab-separated lines `stash find` feeds to fzf
// without spawning fzf itself. Hidden because it's a private helper:
// the in-fzf keybinds invoke it via `reload(...)` so filter changes
// (Ctrl-T tag picker, Alt-R reset) can re-fetch a fresh candidate
// list mid-flight.
//
// Manually invokable too — `stash find-list --tag video > out.txt`
// drops the same lines as the live picker would have shown — useful
// for scripting or sanity-checking what the picker is doing.
var findListCmd = &cobra.Command{
	Use:    "find-list",
	Short:  "Emit the line list `stash find` would feed to fzf (helper for reload bindings)",
	Hidden: true,
	RunE:   runFindList,
}

func init() {
	addSearchFilterFlags(findListCmd)
	rootCmd.AddCommand(findListCmd)
}

func runFindList(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	filter, err := buildFilter(cmd, "")
	if err != nil {
		return err
	}
	if filter.Limit <= 50 {
		filter.Limit = 10000
	}

	items, err := s.ListItems(context.Background(), filter)
	if err != nil {
		return err
	}
	for i := range items {
		fmt.Println(formatFindLine(&items[i]))
	}
	return nil
}
