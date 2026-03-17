package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Manage tags",
}

var tagListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all tags",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		tags, err := s.ListTags(context.Background())
		if err != nil {
			return err
		}

		printTags(tags)
		return nil
	},
}

var tagRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a tag",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if err := s.RenameTag(context.Background(), args[0], args[1]); err != nil {
			return err
		}

		if flagJSON {
			printJSON(map[string]string{"renamed": args[0], "to": args[1]})
		} else {
			fmt.Printf("Renamed tag %q to %q\n", args[0], args[1])
		}
		return nil
	},
}

func init() {
	tagCmd.AddCommand(tagListCmd)
	tagCmd.AddCommand(tagRenameCmd)
	rootCmd.AddCommand(tagCmd)
}
